package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
)

var (
	port     = flag.Int("port", 8080, "HTTP server port")
	homeName = flag.String("home", "家庭A", "Home name")
	subnet   = flag.String("subnet", "", "Subnet to scan (e.g., 192.168.1.0/24)")
	central  = flag.String("central", "", "Central server URL (e.g., http://10.8.0.2:8080)")
	interval = flag.Int("interval", 30000, "Scan interval in milliseconds")
	role     = flag.String("role", "both", "Role: both, server, or scanner")
)

// 常见设备厂商映射 (OUI 前缀)
var ouiMap = map[string]string{
	// Apple
	"3C-CD-57": "Apple iPhone",
	"3C:CD:57": "Apple iPhone",
	"D0-11-E5": "Apple MacBook",
	"D0:11:E5": "Apple MacBook",
	"1A-19-7C": "Apple iPad/Mac",
	"1A:19:7C": "Apple iPad/Mac",
	"F8-10-93": "Apple iPhone",
	"F8:10:93": "Apple iPhone",
	"70-C9-32": "Apple iPhone",
	"70:C9:32": "Apple iPhone",
	"B2-DE-28": "Apple Mac",
	"B2:DE:28": "Apple Mac",
	"6C-71-D2": "Apple iPhone",
	"6C:71:D2": "Apple iPhone",
	"46-66-9A": "Apple Watch",
	"46:66:9A": "Apple Watch",
	"C0-35-32": "Apple Mac",
	"C0:35:32": "Apple Mac",
	"1A-78-30": "Apple TV",
	"1A:78:30": "Apple TV",
	
	// Samsung
	"9C-63-5B": "Samsung",
	"9C:63:5B": "Samsung",
	
	// Huawei
	"00-1E-10": "Huawei",
	"00:1E:10": "Huawei",
	
	// Xiaomi
	"34-80-B3": "Xiaomi",
	"34:80:B3": "Xiaomi",
	
	// TP-Link
	"50-3E-AA": "TP-Link",
	"50:3E:AA": "TP-Link",
	
	// Intel
	"3C-A9-F4": "Intel",
	"3C:A9:F4": "Intel",
	
	// 其他常见
	"00-0C-29": "VMware",
	"00:0C:29": "VMware",
}

// 获取设备类型
func getDeviceType(mac string) string {
	// 去掉冒号和横线，统一格式
	mac = strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(mac, ":", ""), "-", ""))
	
	// 取前8位（前4个字节）
	if len(mac) >= 8 {
		prefix := mac[:2] + "-" + mac[2:4] + "-" + mac[4:6] + "-" + mac[6:8]
		if name, ok := ouiMap[prefix]; ok {
			return name
		}
	}
	
	// 尝试另一种格式
	if len(mac) >= 6 {
		prefix := mac[:2] + ":" + mac[2:4] + ":" + mac[4:6]
		if name, ok := ouiMap[prefix]; ok {
			return name
		}
	}
	
	return ""
}

// 设备状态存储
type Device struct {
	MAC       string `json:"mac"`
	IP        string `json:"ip"`
	Name      string `json:"name,omitempty"`
	Owner     string `json:"owner,omitempty"`
	Home      string `json:"home"`
	Known     bool   `json:"known"`
	Online    bool   `json:"online"`
	LastSeen  int64  `json:"lastSeen"`
	DeviceType string `json:"deviceType,omitempty"`
}

type DeviceReport struct {
	Home      string   `json:"home"`
	Timestamp int64    `json:"timestamp"`
	Devices   []Device `json:"devices"`
}

type HomeStats struct {
	Online int `json:"online"`
	Known  int `json:"known"`
	Total  int `json:"total"`
}

var (
	deviceStore = make(map[string]*Device)
	stats       = make(map[string]HomeStats)
)

// 初始化设备
func initDevice(mac, ip, home string) *Device {
	mac = strings.ToUpper(mac)
	if dev, ok := deviceStore[mac]; ok {
		dev.Online = true
		dev.IP = ip
		dev.LastSeen = time.Now().UnixMilli()
		// 尝试识别设备类型
		if dev.DeviceType == "" {
			dev.DeviceType = getDeviceType(mac)
		}
		return dev
	}
	deviceType := getDeviceType(mac)
	dev := &Device{
		MAC:        mac,
		IP:         ip,
		Home:       home,
		Online:     true,
		LastSeen:   time.Now().UnixMilli(),
		DeviceType: deviceType,
	}
	deviceStore[mac] = dev
	return dev
}

// 获取本机IP和网段
func getLocalNetwork() (string, string) {
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if strings.HasPrefix(iface.Name, "en") || strings.HasPrefix(iface.Name, "eth") {
			addrs, _ := iface.Addrs()
			for _, addr := range addrs {
				if ip, ok := addr.(*net.IPNet); ok && ip.IP.To4() != nil {
					if !ip.IP.IsLoopback() && !strings.HasPrefix(ip.IP.String(), "10.8.") && !strings.HasPrefix(ip.IP.String(), "198.") {
						mask := ip.Mask
						network := ip.IP.Mask(mask).String()
						return network + "/24", iface.Name
					}
				}
			}
		}
	}
	return "", ""
}

// 解析ARP表
func parseArpTable() []Device {
	var devices []Device
	
	var output []byte
	var err error
	
	// 设置超时
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	
	done := make(chan struct{})
	
	go func() {
		if runtime.GOOS == "darwin" {
			output, err = exec.Command("/usr/sbin/arp", "-a").Output()
		} else {
			output, err = exec.Command("arp", "-a").Output()
		}
		close(done)
	}()
	
	select {
	case <-done:
		// 正常完成
	case <-ctx.Done():
		fmt.Println("   ⚠️ ARP表读取超时")
		return devices
	}
	
	if err != nil {
		return devices
	}
	
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		// 跳过 incomplete 的条目
		if strings.Contains(line, "(incomplete)") {
			continue
		}
		
		// 匹配格式: ? (192.168.1.1) at xx:xx:xx:xx:xx:xx on en0
		re := regexp.MustCompile(`\((\d+\.\d+\.\d+\.\d+)\)\s+at\s+([0-9a-fA-F]{2}[:-][0-9a-fA-F]{2}[:-][0-9a-fA-F]{2}[:-][0-9a-fA-F]{2}[:-][0-9a-fA-F]{2}[:-][0-9a-fA-F]{2})`)
		matches := re.FindStringSubmatch(line)
		if len(matches) == 3 {
			ip := matches[1]
			mac := strings.ToUpper(strings.ReplaceAll(matches[2], ":", "-"))
			// 过滤广播地址
			if !strings.Contains(mac, "FF:FF:FF:FF:FF:FF") && !strings.Contains(mac, "FF:FF:FF:FF:FF:FF") {
				devices = append(devices, Device{MAC: mac, IP: ip})
			}
		}
	}
	
	return devices
}

// 扫描局域网
func scanNetwork(subnetStr string) []Device {
	// 先尝试ARP表
	devChan := make(chan []Device, 1)
	
	// 启动ARP读取 goroutine
	go func() {
		devices := parseArpTable()
		devChan <- devices
	}()
	
	// 等待ARP结果，最多2秒
	select {
	case devices := <-devChan:
		if len(devices) > 0 {
			fmt.Printf("   ARP表发现 %d 个设备\n", len(devices))
			return devices
		}
	case <-time.After(2 * time.Second):
		fmt.Println("   ARP表读取超时")
	}
	
	// ARP为空或超时，返回空（不进行ping扫描，节省时间）
	fmt.Println("   ARP表为空，跳过ping扫描")
	return []Device{}
}

// 上报中央服务器
func reportToCentral(devices []Device) {
	if *central == "" {
		return
	}
	
	report := DeviceReport{
		Home:      *homeName,
		Timestamp: time.Now().UnixMilli(),
		Devices:   devices,
	}
	
	body, _ := json.Marshal(report)
	resp, err := http.Post(*central+"/api/report", "application/json", strings.NewReader(string(body)))
	if err != nil {
		fmt.Printf("   ❌ 上报失败: %v\n", err)
		return
	}
	defer resp.Body.Close()
	
	fmt.Printf("   📤 已上报 %d 个设备\n", len(devices))
}

// 扫描循环
func scanLoop() {
	subnetStr := *subnet
	if subnetStr == "" {
		net, iface := getLocalNetwork()
		if net != "" {
			subnetStr = net
			fmt.Printf("🔍 检测到局域网: %s (接口: %s)\n", subnetStr, iface)
		}
	}
	
	if subnetStr == "" {
		subnetStr = "192.168.1.0/24"
		fmt.Printf("🔍 使用默认网段: %s\n", subnetStr)
	}
	
	fmt.Printf("🏠 设备扫描器启动\n")
	fmt.Printf("   家庭: %s\n", *homeName)
	fmt.Printf("   网段: %s\n", subnetStr)
	fmt.Printf("   目标: %s\n", *central)
	fmt.Printf("   间隔: %dms\n\n", *interval)
	
	ticker := time.NewTicker(time.Duration(*interval) * time.Millisecond)
	defer ticker.Stop()
	
	// 立即执行一次
	runScan(subnetStr)
	
	for range ticker.C {
		runScan(subnetStr)
	}
}

func runScan(subnetStr string) {
	now := time.Now().Format("3:04:05 PM")
	fmt.Printf("[%s] 🔍 扫描 %s...\n", now, *homeName)
	
	devices := scanNetwork(subnetStr)
	fmt.Printf("   发现 %d 个设备\n", len(devices))
	
	for _, d := range devices {
		fmt.Printf("   - %s  %s", d.IP, d.MAC)
		if d.DeviceType != "" {
			fmt.Printf("  [%s]", d.DeviceType)
		}
		fmt.Println()
		initDevice(d.MAC, d.IP, *homeName)
	}
	
	reportToCentral(devices)
}

// HTTP服务
func startServer() {
	// API: 上报设备
	http.HandleFunc("/api/report", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		
		body, _ := io.ReadAll(r.Body)
		var report DeviceReport
		if err := json.Unmarshal(body, &report); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		
		fmt.Printf("📥 收到 %s 上报: %d 个设备\n", report.Home, len(report.Devices))
		
		// 更新设备状态
		for _, d := range report.Devices {
			dev := initDevice(d.MAC, d.IP, report.Home)
			dev.Online = true
			dev.LastSeen = time.Now().UnixMilli()
			if d.DeviceType != "" {
				dev.DeviceType = d.DeviceType
			}
		}
		
		// 更新统计
		updateStats()
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	
	// API: 获取设备列表
	http.HandleFunc("/api/devices", func(w http.ResponseWriter, r *http.Request) {
		updateStats()
		
		var devices []Device
		for _, d := range deviceStore {
			devices = append(devices, *d)
		}
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"devices": devices,
			"stats":   stats,
		})
	})
	
	// API: 更新设备
	http.HandleFunc("/api/devices/", func(w http.ResponseWriter, r *http.Request) {
		mac := strings.TrimPrefix(r.URL.Path, "/api/devices/")
		mac = strings.ToUpper(mac)
		
		if r.Method == http.MethodPut {
			var req struct {
				Name  string `json:"name"`
				Owner string `json:"owner"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			
			if dev, ok := deviceStore[mac]; ok {
				dev.Name = req.Name
				dev.Owner = req.Owner
				dev.Known = req.Owner != ""
				
				// 保存到文件
				saveDevices()
				
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "device": dev})
				return
			}
		}
		
		http.Error(w, "not found", http.StatusNotFound)
	})
	
	// Web UI
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		html := `<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>家庭设备监控</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, sans-serif; background: #f5f5f5; padding: 20px; }
    .container { max-width: 900px; margin: 0 auto; }
    h1 { text-align: center; margin-bottom: 30px; }
    .stats { display: flex; gap: 20px; justify-content: center; margin-bottom: 30px; }
    .stat-card { background: white; padding: 20px; border-radius: 12px; text-align: center; box-shadow: 0 2px 8px rgba(0,0,0,0.1); }
    .count { font-size: 36px; font-weight: bold; color: #2563eb; }
    .device-list { display: grid; gap: 12px; }
    .device-card { background: white; padding: 15px; border-radius: 8px; border-left: 4px solid #22c55e; }
    .device-card.offline { border-left-color: #999; opacity: 0.6; }
    .device-card.unknown { border-left-color: #f59e0b; }
    .device-header { display: flex; justify-content: space-between; align-items: center; }
    .device-name { font-weight: 600; }
    .device-type { font-size: 12px; color: #2563eb; background: #eff6ff; padding: 2px 8px; border-radius: 10px; margin-left: 8px; }
    .device-info { color: #666; font-size: 13px; margin-top: 5px; }
    .status { font-size: 12px; padding: 2px 8px; border-radius: 10px; background: #dcfce7; color: #166534; }
    .status.offline { background: #f3f4f6; color: #666; }
    .empty { text-align: center; padding: 40px; color: #999; }
    .home-section { background: white; padding: 20px; border-radius: 12px; margin-bottom: 20px; box-shadow: 0 2px 8px rgba(0,0,0,0.1); }
    .home-section h2 { margin-bottom: 15px; padding-bottom: 10px; border-bottom: 1px solid #eee; }
  </style>
</head>
<body>
  <div class="container">
    <h1>🏠 家庭设备监控</h1>
    <div class="stats" id="stats"></div>
    <div id="deviceList"></div>
  </div>
  <script>
    function loadData() {
      fetch('/api/devices').then(r=>r.json()).then(data => {
        document.getElementById('stats').innerHTML = Object.entries(data.stats).map(([home, s]) =>
          '<div class="stat-card"><div>'+home+'</div><div class="count">'+s.online+'</div><div>在线 / 共 '+s.total+' 台</div></div>'
        ).join('');
        
        const byHome = {};
        data.devices.forEach(d => { byHome[d.home] = byHome[d.home] || []; byHome[d.home].push(d); });
        
        document.getElementById('deviceList').innerHTML = Object.entries(byHome).map(([home, devs]) =>
          '<div class="home-section"><h2>'+home+' ('+devs.filter(d=>d.online).length+' 在线)</h2><div class="device-list">' +
          devs.map(d => '<div class="device-card '+(d.online?'':'offline')+' '+(d.known?'':'unknown')+'"><div class="device-header"><span class="device-name">'+(d.name||d.deviceType||'未知设备')+(d.deviceType && d.name?' ('+d.deviceType+')':'')+'</span> <span class="status">'+(d.online?'在线':'离线')+'</span></div><div class="device-info">IP: '+d.ip+' | MAC: '+d.mac+(d.owner?' | '+d.owner:'')+'</div></div>').join('') +
          '</div></div>'
        ).join('');
      });
    }
    loadData();
    setInterval(loadData, 10000);
  </script>
</body>
</html>`
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, html)
	})
	
	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("🏠 中央服务启动: http://localhost:%d\n", *port)
	fmt.Printf("   Web UI: http://localhost:%d\n", *port)
	
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Printf("Server error: %v\n", err)
	}
}

func updateStats() {
	stats = make(map[string]HomeStats)
	for _, d := range deviceStore {
		s := stats[d.Home]
		s.Total++
		if d.Online {
			s.Online++
			if d.Known {
				s.Known++
			}
		}
		stats[d.Home] = s
	}
}

func saveDevices() {
	file, _ := os.Create("data/devices.json")
	defer file.Close()
	json.NewEncoder(file).Encode(deviceStore)
}

func loadDevices() {
	file, err := os.Open("data/devices.json")
	if err != nil {
		return
	}
	defer file.Close()
	json.NewDecoder(file).Decode(&deviceStore)
}

func main() {
	flag.Parse()
	
	// 创建数据目录
	os.MkdirAll("data", 0755)
	loadDevices()
	
	// 优雅退出
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n👋 保存数据并退出...")
		saveDevices()
		os.Exit(0)
	}()
	
	// 根据角色启动
	if *role == "scanner" || *role == "both" {
		go scanLoop()
	}
	
	if *role == "server" || *role == "both" {
		startServer()
	}
	
	// 保持运行
	select {}
}

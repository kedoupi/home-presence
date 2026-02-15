package main

import (
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

// 设备状态存储
type Device struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Name     string `json:"name,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Home     string `json:"home"`
	Known    bool   `json:"known"`
	Online   bool   `json:"online"`
	LastSeen int64  `json:"lastSeen"`
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
		return dev
	}
	dev := &Device{
		MAC:      mac,
		IP:       ip,
		Home:     home,
		Online:   true,
		LastSeen: time.Now().UnixMilli(),
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
	
	// 尝试读取ARP表
	var output []byte
	var err error
	
	if runtime.GOOS == "darwin" {
		output, err = exec.Command("/usr/sbin/arp", "-a").Output()
	} else {
		output, err = exec.Command("arp", "-a").Output()
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
		// 使用正则表达式
		re := regexp.MustCompile(`\((\d+\.\d+\.\d+\.\d+)\)\s+at\s+([0-9a-fA-F]{2}[:-][0-9a-fA-F]{2}[:-][0-9a-fA-F]{2}[:-][0-9a-fA-F]{2}[:-][0-9a-fA-F]{2}[:-][0-9a-fA-F]{2})`)
		matches := re.FindStringSubmatch(line)
		if len(matches) == 3 {
			ip := matches[1]
			mac := strings.ToUpper(strings.ReplaceAll(matches[2], ":", "-"))
			devices = append(devices, Device{MAC: mac, IP: ip})
		}
	}
	
	return devices
}

// 扫描局域网
func scanNetwork(subnetStr string) []Device {
	// 先尝试ARP表
	devices := parseArpTable()
	if len(devices) > 0 {
		fmt.Printf("   ARP表发现 %d 个设备\n", len(devices))
		return devices
	}
	
	// ARP为空，用ping扫描
	fmt.Println("   ARP表为空，使用ping扫描...")
	
	// 解析网段
	parts := strings.Split(strings.Split(subnetStr, "/")[0], ".")
	base := parts[0] + "." + parts[1] + "." + parts[2]
	
	// 并发ping
	ch := make(chan Device, 254)
	concurrency := 50
	sem := make(chan struct{}, concurrency)
	
	for i := 1; i <= 254; i++ {
		ip := fmt.Sprintf("%s.%d", base, i)
		go func(ip string) {
			sem <- struct{}{}
			defer func() { <-sem }()
			
			var cmd *exec.Cmd
			if runtime.GOOS == "darwin" {
				cmd = exec.Command("/sbin/ping", "-c", "1", "-t", "1", ip)
			} else {
				cmd = exec.Command("ping", "-c", "1", "-W", "1", ip)
			}
			
			if err := cmd.Run(); err == nil {
				// ping成功，尝试获取MAC
				arpCmd := exec.Command("arp", "-n", ip)
				arpOut, _ := arpCmd.Output()
				arpStr := string(arpOut)
				
				var mac string
				fmt.Sscanf(arpStr, "%s %s %s", &mac, &mac, &mac)
				if mac != "" && !strings.Contains(mac, "ff:ff:ff:ff:ff:ff") {
					mac = strings.ToUpper(strings.ReplaceAll(mac, ":", "-"))
					ch <- Device{MAC: mac, IP: ip}
				}
			}
		}(ip)
	}
	
	time.Sleep(2 * time.Second)
	close(ch)
	
	for dev := range ch {
		devices = append(devices, dev)
	}
	
	return devices
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
		fmt.Printf("   - %s  %s\n", d.IP, d.MAC)
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
    .container { max-width: 800px; margin: 0 auto; }
    h1 { text-align: center; margin-bottom: 30px; }
    .stats { display: flex; gap: 20px; justify-content: center; margin-bottom: 30px; }
    .stat-card { background: white; padding: 20px; border-radius: 12px; text-align: center; box-shadow: 0 2px 8px rgba(0,0,0,0.1); }
    .count { font-size: 36px; font-weight: bold; color: #2563eb; }
    .device-list { display: grid; gap: 12px; }
    .device-card { background: white; padding: 15px; border-radius: 8px; border-left: 4px solid #22c55e; }
    .device-card.offline { border-left-color: #999; opacity: 0.6; }
    .device-card.unknown { border-left-color: #f59e0b; }
    .device-name { font-weight: 600; }
    .device-info { color: #666; font-size: 13px; margin-top: 5px; }
    .status { font-size: 12px; padding: 2px 8px; border-radius: 10px; background: #dcfce7; color: #166534; }
    .status.offline { background: #f3f4f6; color: #666; }
    .empty { text-align: center; padding: 40px; color: #999; }
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
          '<div style="background:white;padding:20px;border-radius:12px;margin-bottom:20px;box-shadow:0 2px 8px rgba(0,0,0,0.1)"><h2>'+home+'</h2><div class="device-list">' +
          devs.map(d => '<div class="device-card '+(d.online?'':'offline')+' '+(d.known?'':'unknown')+'"><div><span class="device-name">'+(d.name||'未知设备')+'</span> <span class="status">'+(d.online?'在线':'离线')+'</span></div><div class="device-info">IP: '+d.ip+' | MAC: '+d.mac+(d.owner?' | '+d.owner:'')+'</div></div>').join('') +
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

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
	"strings"
	"syscall"
	"time"
)

var (
	port     = flag.Int("port", 8080, "HTTP server port")
	homeName = flag.String("home", "家庭A", "Home name")
	subnet   = flag.String("subnet", "", "Subnet to scan")
	central  = flag.String("central", "", "Central server URL")
	interval = flag.Int("interval", 30000, "Scan interval in ms")
	role     = flag.String("role", "both", "Role: both, server, or scanner")
)

var ouiMap = map[string]string{
	"3C-CD-57": "Apple iPhone", "3C:CD:57": "Apple iPhone",
	"D0-11-E5": "Apple MacBook", "D0:11:E5": "Apple MacBook",
	"1A-19-7C": "Apple iPad/Mac", "1A:19:7C": "Apple iPad/Mac",
	"F8-10-93": "Apple iPhone", "F8:10:93": "Apple iPhone",
	"70-C9-32": "Apple iPhone", "70:C9:32": "Apple iPhone",
	"B2-DE-28": "Apple Mac", "B2:DE:28": "Apple Mac",
	"6C-71-D2": "Apple iPhone", "6C:71:D2": "Apple iPhone",
	"46-66-9A": "Apple Watch", "46:66:9A": "Apple Watch",
	"C0-35-32": "Apple Mac", "C0:35:32": "Apple Mac",
	"1A-78-30": "Apple TV", "1A:78:30": "Apple TV",
	"9C-63-5B": "Samsung", "9C:63:5B": "Samsung",
}

func getDeviceType(mac string) string {
	mac = strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(mac, ":", ""), "-", ""))
	if len(mac) >= 8 {
		prefix := mac[:2] + "-" + mac[2:4] + "-" + mac[4:6] + "-" + mac[6:8]
		if name, ok := ouiMap[prefix]; ok {
			return name
		}
	}
	return ""
}

type Device struct {
	MAC        string `json:"mac"`
	IP         string `json:"ip"`
	Name       string `json:"name,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Home       string `json:"home"`
	Known      bool   `json:"known"`
	Online     bool   `json:"online"`
	LastSeen   int64  `json:"lastSeen"`
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

var deviceStore = make(map[string]*Device)
var stats = make(map[string]HomeStats)

func initDevice(mac, ip, home string) *Device {
	mac = strings.ToUpper(mac)
	if dev, ok := deviceStore[mac]; ok {
		dev.Online = true
		dev.IP = ip
		dev.LastSeen = time.Now().UnixMilli()
		if dev.DeviceType == "" {
			dev.DeviceType = getDeviceType(mac)
		}
		return dev
	}
	dev := &Device{
		MAC:        mac,
		IP:         ip,
		Home:       home,
		Online:     true,
		LastSeen:   time.Now().UnixMilli(),
		DeviceType: getDeviceType(mac),
	}
	deviceStore[mac] = dev
	return dev
}

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

func parseArpTable() []Device {
	var devices []Device
	output, _ := exec.Command("sh", "-c", "timeout 2 /usr/sbin/arp -a 2>/dev/null || timeout 2 arp -a 2>/dev/null || echo ''").Output()
	if len(output) == 0 {
		return devices
	}
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "(incomplete)") {
			continue
		}
		re := regexp.MustCompile(`\((\d+\.\d+\.\d+\.\d+)\)\s+at\s+([0-9a-fA-F]{1,2}[:][0-9a-fA-F]{1,2}[:][0-9a-fA-F]{1,2}[:][0-9a-fA-F]{1,2}[:][0-9a-fA-F]{1,2}[:][0-9a-fA-F]{1,2})`)
		matches := re.FindStringSubmatch(line)
		if len(matches) == 3 {
			ip := matches[1]
			mac := matches[2]
			macParts := strings.Split(mac, ":")
			var standardized []string
			for _, p := range macParts {
				if len(p) == 1 {
					p = "0" + p
				}
				standardized = append(standardized, strings.ToUpper(p))
			}
			mac = strings.Join(standardized, "-")
			if !strings.Contains(mac, "FF:FF:FF:FF:FF:FF") {
				dev := Device{MAC: mac, IP: ip, DeviceType: getDeviceType(mac)}
				if saved, ok := deviceStore[mac]; ok && saved.Name != "" {
					dev.Name = saved.Name
					dev.Owner = saved.Owner
					dev.Known = saved.Known
				}
				devices = append(devices, dev)
			}
		}
	}
	return devices
}

func scanNetwork(subnetStr string) []Device {
	devChan := make(chan []Device, 1)
	go func() {
		devChan <- parseArpTable()
	}()
	select {
	case devices := <-devChan:
		// 尝试通过mDNS获取设备名称
		mdnsNames := scanMDNS()
		for i := range devices {
			if name, ok := mdnsNames[devices[i].IP]; ok && devices[i].Name == "" {
				devices[i].Name = name
			}
		}
		return devices
	case <-time.After(3 * time.Second):
		fmt.Println("   ARP表读取超时")
		return []Device{}
	}
}

// 通过NetBIOS扫描获取设备名称
func scanMDNS() map[string]string {
	result := make(map[string]string)
	
	output, _ := exec.Command("sh", "-c", "arp -a | grep -v incomplete").Output()
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		ipRe := regexp.MustCompile(`\((\d+\.\d+\.\d+\.\d+)\)`)
		matches := ipRe.FindStringSubmatch(line)
		if len(matches) == 2 {
			ip := matches[1]
			// 尝试nmblookup获取NetBIOS名称
			nbOutput, _ := exec.Command("nmblookup", "-A", ip).Output()
			nbRe := regexp.MustCompile(`(\S+)<00>`)
			nbMatches := nbRe.FindStringSubmatch(string(nbOutput))
			if len(nbMatches) == 2 && len(nbMatches[1]) < 16 {
				result[ip] = nbMatches[1]
			}
		}
	}
	
	return result
}

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
	}

	fmt.Printf("🏠 设备扫描器启动\n")
	fmt.Printf("   家庭: %s\n", *homeName)
	fmt.Printf("   网段: %s\n", subnetStr)
	fmt.Printf("   目标: %s\n", *central)
	fmt.Printf("   间隔: %dms\n\n", *interval)

	ticker := time.NewTicker(time.Duration(*interval) * time.Millisecond)
	defer ticker.Stop()

	runScan(subnetStr)
	for range ticker.C {
		runScan(subnetStr)
	}
}

func runScan(subnetStr string) {
	now := time.Now().Format("3:04:05 PM")
	fmt.Printf("[%s] 🔍 扫描 %s...\n", now, *homeName)

	devices := scanNetwork(subnetStr)
	if len(devices) == 0 {
		fmt.Printf("   ARP表为空，跳过ping扫描\n")
	}
	fmt.Printf("   发现 %d 个设备\n", len(devices))

	for _, d := range devices {
		fmt.Printf("   - %s %s", d.IP, d.MAC)
		if d.DeviceType != "" {
			fmt.Printf(" [%s]", d.DeviceType)
		}
		fmt.Println()
		initDevice(d.MAC, d.IP, *homeName)
	}

	reportToCentral(devices)
}

func startServer() {
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
		for _, d := range report.Devices {
			dev := initDevice(d.MAC, d.IP, report.Home)
			dev.Online = true
			dev.LastSeen = time.Now().UnixMilli()
			if d.DeviceType != "" {
				dev.DeviceType = d.DeviceType
			}
		}
		updateStats()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	http.HandleFunc("/api/devices", func(w http.ResponseWriter, r *http.Request) {
		updateStats()
		var devices []Device
		for _, d := range deviceStore {
			devices = append(devices, *d)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"devices": devices, "stats": stats})
	})

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
				saveDevices()
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "device": dev})
				return
			}
		}
		http.Error(w, "not found", http.StatusNotFound)
	})

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
    .home-links { display: flex; gap: 20px; justify-content: center; margin-bottom: 30px; }
    .home-link { background: white; padding: 20px 40px; border-radius: 12px; text-align: center; box-shadow: 0 2px 8px rgba(0,0,0,0.1); cursor: pointer; }
    .home-link:hover { box-shadow: 0 4px 12px rgba(0,0,0,0.15); }
    .home-name { font-size: 18px; font-weight: 600; }
    .home-count { font-size: 32px; font-weight: bold; color: #2563eb; }
    .home-sub { font-size: 13px; color: #999; }
    .back-btn { display: inline-block; padding: 8px 16px; background: #f3f4f6; border-radius: 6px; margin-bottom: 20px; cursor: pointer; }
    .device-list { display: grid; gap: 12px; }
    .device-card { background: white; padding: 15px; border-radius: 8px; border-left: 4px solid #22c55e; cursor: pointer; }
    .device-card:hover { box-shadow: 0 2px 8px rgba(0,0,0,0.1); }
    .device-card.offline { border-left-color: #999; opacity: 0.6; }
    .device-header { display: flex; justify-content: space-between; align-items: center; }
    .device-name { font-weight: 600; }
    .status { font-size: 12px; padding: 2px 8px; border-radius: 10px; background: #dcfce7; color: #166534; }
    .status.offline { background: #f3f4f6; color: #666; }
    .device-info { color: #666; font-size: 13px; margin-top: 5px; }
    .modal { display: none; position: fixed; top: 0; left: 0; right: 0; bottom: 0; background: rgba(0,0,0,0.5); align-items: center; justify-content: center; z-index: 1000; }
    .modal.show { display: flex; }
    .modal-content { background: white; padding: 24px; border-radius: 12px; width: 320px; }
    .form-group { margin-bottom: 12px; }
    .form-group label { display: block; font-size: 13px; color: #666; margin-bottom: 4px; }
    .form-group input { width: 100%; padding: 8px; border: 1px solid #ddd; border-radius: 6px; box-sizing: border-box; }
    .btn-group { display: flex; gap: 8px; margin-top: 16px; }
    .btn { flex: 1; padding: 8px 16px; border: none; border-radius: 6px; cursor: pointer; }
    .btn-primary { background: #2563eb; color: white; }
    .btn-secondary { background: #f3f4f6; color: #666; }
  </style>
</head>
<body>
  <div class="container">
    <h1>🏠 家庭设备监控</h1>
    <div id="homeLinks"></div>
    <div id="deviceList"></div>
  </div>
  <div class="modal" id="editModal">
    <div class="modal-content">
      <h3>编辑设备</h3>
      <div class="form-group"><label>MAC</label><input type="text" id="editMac" readonly></div>
      <div class="form-group"><label>名称</label><input type="text" id="editName" placeholder="如：客厅空调"></div>
      <div class="form-group"><label>所属人</label><input type="text" id="editOwner" placeholder="如：张老板"></div>
      <div class="btn-group">
        <button class="btn btn-primary" onclick="saveDevice()">保存</button>
        <button class="btn btn-secondary" onclick="closeModal()">取消</button>
      </div>
    </div>
  </div>
  <script>
    let allDevices = [];
    let editingMac = '';
    let currentHome = '';
    
    function loadData() {
      fetch('/api/devices').then(r=>r.json()).then(data => {
        allDevices = data.devices;
        const homes = Object.keys(data.stats);
        document.getElementById('homeLinks').innerHTML = homes.map(h => {
          const s = data.stats[h];
          return '<div class="home-link" onclick="showHome(\''+h+'\')"><div class="home-name">'+h+'</div><div class="home-count">'+s.online+'</div><div class="home-sub">在线 / 共 '+s.total+' 台</div></div>';
        }).join('');
        if (currentHome) showHome(currentHome);
      });
    }
    
    function showHome(home) {
      currentHome = home;
      const devs = allDevices.filter(d => d.home === home);
      const online = devs.filter(d => d.online).length;
      document.getElementById('homeLinks').innerHTML = '<div class="back-btn" onclick="goHome()">← 返回首页</div>';
      document.getElementById('deviceList').innerHTML = '<div class="home-section"><h2>'+home+' ('+online+' 在线)</h2><div class="device-list">' +
        devs.map(d => '<div class="device-card '+(d.online?'':'offline')+'" onclick="editDevice(\''+d.mac+'\')"><div class="device-header"><span class="device-name">'+(d.name||d.deviceType||'未知设备')+'</span><span class="status '+(d.online?'':'offline')+'">'+(d.online?'在线':'离线')+'</span></div><div class="device-info">IP: '+d.ip+' | MAC: '+d.mac+(d.owner?' | '+d.owner:'')+'</div></div>').join('') + '</div></div>';
    }
    
    function goHome() { currentHome = ''; loadData(); }
    
    function editDevice(mac) {
      const dev = allDevices.find(d => d.mac === mac);
      if (!dev) return;
      editingMac = mac;
      document.getElementById('editMac').value = dev.mac;
      document.getElementById('editName').value = dev.name || '';
      document.getElementById('editOwner').value = dev.owner || '';
      document.getElementById('editModal').classList.add('show');
    }
    
    function closeModal() { document.getElementById('editModal').classList.remove('show'); }
    
    function saveDevice() {
      const name = document.getElementById('editName').value;
      const owner = document.getElementById('editOwner').value;
      fetch('/api/devices/' + editingMac, { method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({name, owner}) })
        .then(() => { closeModal(); loadData(); });
    }
    
    loadData();
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
	os.MkdirAll("data", 0755)
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
	os.MkdirAll("data", 0755)
	loadDevices()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n👋 保存数据并退出...")
		saveDevices()
		os.Exit(0)
	}()

	if *role == "scanner" || *role == "both" {
		go scanLoop()
	}

	if *role == "server" || *role == "both" {
		startServer()
	}

	select {}
}

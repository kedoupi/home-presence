package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed static/index.html
var indexHTML []byte

// Command-line flags
var (
	port     = flag.Int("port", 8080, "HTTP server port")
	homeName = flag.String("home", "家庭A", "Home name")
	subnet   = flag.String("subnet", "", "Subnet to scan (auto-detect if empty)")
	central  = flag.String("central", "", "Central server URL (scanner mode)")
	interval = flag.Int("interval", 30000, "Scan interval in milliseconds")
	role     = flag.String("role", "both", "Role: both, server, or scanner")
)

// Pre-compiled regexes (avoid recompilation in loops)
var (
	reArpEntry = regexp.MustCompile(`\((\d+\.\d+\.\d+\.\d+)\)\s+at\s+([0-9a-fA-F]{1,2}:[0-9a-fA-F]{1,2}:[0-9a-fA-F]{1,2}:[0-9a-fA-F]{1,2}:[0-9a-fA-F]{1,2}:[0-9a-fA-F]{1,2})`)
	reNetBIOS  = regexp.MustCompile(`(\S+)<00>`)
)

// OUI prefix → device type (3-byte vendor prefix, dash-separated uppercase)
var ouiMap = map[string]string{
	"3C-CD-57": "Apple iPhone",
	"D0-11-E5": "Apple MacBook",
	"1A-19-7C": "Apple iPad/Mac",
	"F8-10-93": "Apple iPhone",
	"70-C9-32": "Apple iPhone",
	"B2-DE-28": "Apple Mac",
	"6C-71-D2": "Apple iPhone",
	"46-66-9A": "Apple Watch",
	"C0-35-32": "Apple Mac",
	"1A-78-30": "Apple TV",
	"9C-63-5B": "Samsung",
}

// --- Models ---

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

// --- Global state (mutex-protected) ---

var (
	deviceStore = make(map[string]*Device)
	stats       = make(map[string]HomeStats)
	mu          sync.RWMutex
)

// --- MAC helpers ---

// rawMAC strips separators and uppercases: "aa:bb:cc:dd:ee:ff" -> "AABBCCDDEEFF"
func rawMAC(mac string) string {
	return strings.ToUpper(strings.NewReplacer(":", "", "-", "").Replace(mac))
}

// normalizeMac converts any MAC format to uppercase dash-separated: "AA-BB-CC-DD-EE-FF"
func normalizeMac(mac string) string {
	raw := rawMAC(mac)
	if len(raw) != 12 {
		return strings.ToUpper(mac)
	}
	parts := make([]string, 6)
	for i := 0; i < 6; i++ {
		parts[i] = raw[i*2 : i*2+2]
	}
	return strings.Join(parts, "-")
}

// getDeviceType returns a human-readable device type based on OUI prefix.
func getDeviceType(mac string) string {
	raw := rawMAC(mac)
	if len(raw) >= 6 {
		prefix := raw[:2] + "-" + raw[2:4] + "-" + raw[4:6]
		if name, ok := ouiMap[prefix]; ok {
			return name
		}
	}
	return ""
}

// --- Device store operations ---

func initDevice(mac, ip, home string) *Device {
	mac = normalizeMac(mac)
	mu.Lock()
	defer mu.Unlock()
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

func updateStats() {
	mu.Lock()
	defer mu.Unlock()
	newStats := make(map[string]HomeStats)
	for _, d := range deviceStore {
		s := newStats[d.Home]
		s.Total++
		if d.Online {
			s.Online++
			if d.Known {
				s.Known++
			}
		}
		newStats[d.Home] = s
	}
	stats = newStats
}

// --- Persistence (atomic write) ---

func saveDevices() {
	mu.RLock()
	data, err := json.MarshalIndent(deviceStore, "", "  ")
	mu.RUnlock()
	if err != nil {
		log.Printf("Failed to marshal devices: %v", err)
		return
	}
	if err := os.MkdirAll("data", 0755); err != nil {
		log.Printf("Failed to create data dir: %v", err)
		return
	}
	// Atomic write: write to temp file, then rename
	tmpPath := filepath.Join("data", "devices.json.tmp")
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		log.Printf("Failed to write temp file: %v", err)
		return
	}
	if err := os.Rename(tmpPath, filepath.Join("data", "devices.json")); err != nil {
		log.Printf("Failed to rename temp file: %v", err)
	}
}

func loadDevices() {
	mu.Lock()
	defer mu.Unlock()
	data, err := os.ReadFile(filepath.Join("data", "devices.json"))
	if err != nil {
		return
	}
	if err := json.Unmarshal(data, &deviceStore); err != nil {
		log.Printf("Failed to parse devices.json: %v", err)
	}
}

// --- Network detection ---

func getLocalNetwork() (string, string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		log.Printf("Failed to list interfaces: %v", err)
		return "", ""
	}
	for _, iface := range ifaces {
		if !strings.HasPrefix(iface.Name, "en") && !strings.HasPrefix(iface.Name, "eth") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil || ipNet.IP.IsLoopback() {
				continue
			}
			ipStr := ipNet.IP.String()
			if strings.HasPrefix(ipStr, "10.8.") || strings.HasPrefix(ipStr, "198.") {
				continue
			}
			network := ipNet.IP.Mask(ipNet.Mask).String()
			return network + "/24", iface.Name
		}
	}
	return "", ""
}

func getLocalIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if !strings.HasPrefix(iface.Name, "en") && !strings.HasPrefix(iface.Name, "eth") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if ok && ipNet.IP.To4() != nil && !ipNet.IP.IsLoopback() {
				return ipNet.IP.String()
			}
		}
	}
	return ""
}

// --- Scanner ---

func parseArpTable() []Device {
	var devices []Device
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "arp", "-a").Output()
	if err != nil {
		log.Printf("ARP command failed: %v", err)
		return devices
	}
	if len(output) == 0 {
		return devices
	}
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "(incomplete)") {
			continue
		}
		matches := reArpEntry.FindStringSubmatch(line)
		if len(matches) != 3 {
			continue
		}
		ip := matches[1]
		mac := normalizeMac(matches[2])
		if mac == "FF-FF-FF-FF-FF-FF" {
			continue
		}
		dev := Device{MAC: mac, IP: ip, DeviceType: getDeviceType(mac)}
		mu.RLock()
		if saved, ok := deviceStore[mac]; ok && saved.Name != "" {
			dev.Name = saved.Name
			dev.Owner = saved.Owner
			dev.Known = saved.Known
		}
		mu.RUnlock()
		devices = append(devices, dev)
	}
	return devices
}

// scanNetBIOS queries NetBIOS names for a list of IPs via nmblookup.
func scanNetBIOS(ips []string) map[string]string {
	result := make(map[string]string)
	for _, ip := range ips {
		output, err := exec.Command("nmblookup", "-A", ip).Output()
		if err != nil {
			continue
		}
		matches := reNetBIOS.FindStringSubmatch(string(output))
		if len(matches) == 2 && len(matches[1]) < 16 {
			result[ip] = matches[1]
		}
	}
	return result
}

func scanNetwork() []Device {
	devChan := make(chan []Device, 1)
	go func() {
		devChan <- parseArpTable()
	}()
	select {
	case devices := <-devChan:
		var ips []string
		for _, d := range devices {
			if d.Name == "" {
				ips = append(ips, d.IP)
			}
		}
		if len(ips) > 0 {
			nbNames := scanNetBIOS(ips)
			for i := range devices {
				if name, ok := nbNames[devices[i].IP]; ok && devices[i].Name == "" {
					devices[i].Name = name
				}
			}
		}
		return devices
	case <-time.After(5 * time.Second):
		log.Println("ARP table read timeout")
		return nil
	}
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
	body, err := json.Marshal(report)
	if err != nil {
		log.Printf("Failed to marshal report: %v", err)
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(*central+"/api/report", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("Report failed: %v", err)
		return
	}
	resp.Body.Close()
	log.Printf("Reported %d devices to central", len(devices))
}

func scanLoop(ctx context.Context) {
	subnetStr := *subnet
	if subnetStr == "" {
		detectedNet, iface := getLocalNetwork()
		if detectedNet != "" {
			subnetStr = detectedNet
			log.Printf("Detected LAN: %s (interface: %s)", subnetStr, iface)
		}
	}
	if subnetStr == "" {
		subnetStr = "192.168.1.0/24"
	}

	log.Printf("Scanner started: home=%s subnet=%s interval=%dms", *homeName, subnetStr, *interval)

	runScan()

	ticker := time.NewTicker(time.Duration(*interval) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runScan()
		}
	}
}

func markAllOffline(home string) {
	mu.Lock()
	defer mu.Unlock()
	for _, d := range deviceStore {
		if d.Home == home {
			d.Online = false
		}
	}
}

func runScan() {
	log.Printf("Scanning %s...", *homeName)

	devices := scanNetwork()
	log.Printf("Found %d devices", len(devices))

	markAllOffline(*homeName)
	for _, d := range devices {
		if d.DeviceType != "" {
			log.Printf("  %s %s [%s]", d.IP, d.MAC, d.DeviceType)
		}
		initDevice(d.MAC, d.IP, *homeName)
	}
	updateStats()
	reportToCentral(devices)
}

// --- HTTP Server ---

func startServer(ctx context.Context) {
	mux := http.NewServeMux()

	// POST /api/report — scanner nodes report devices
	mux.HandleFunc("/api/report", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Limit request body to 1MB
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var report DeviceReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		log.Printf("Received report from %s: %d devices", report.Home, len(report.Devices))
		for _, d := range report.Devices {
			initDevice(d.MAC, d.IP, report.Home)
		}
		updateStats()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// GET /api/devices — list all devices + stats
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		devices := make([]Device, 0, len(deviceStore))
		for _, d := range deviceStore {
			devices = append(devices, *d)
		}
		s := stats
		mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"devices": devices, "stats": s})
	})

	// PUT /api/devices/:mac — update device name/owner
	mux.HandleFunc("/api/devices/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		mac := normalizeMac(strings.TrimPrefix(r.URL.Path, "/api/devices/"))
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Name  string `json:"name"`
			Owner string `json:"owner"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		mu.Lock()
		dev, ok := deviceStore[mac]
		if !ok {
			mu.Unlock()
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		dev.Name = req.Name
		dev.Owner = req.Owner
		dev.Known = req.Owner != ""
		snapshot := *dev
		mu.Unlock()
		saveDevices()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "device": snapshot})
	})

	// GET / — serve embedded Web UI
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", *port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		log.Println("Shutting down HTTP server...")
		server.Shutdown(shutdownCtx)
	}()

	log.Printf("Server started: http://localhost:%d", *port)
	if localIP := getLocalIP(); localIP != "" {
		log.Printf("Scanner connect command:")
		log.Printf("  ./home-presence -role scanner -home <name> -central http://%s:%d", localIP, *port)
	}
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

// --- Main ---

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime)

	if err := os.MkdirAll("data", 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}
	loadDevices()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received %v, shutting down...", sig)
		saveDevices()
		cancel()
	}()

	if *role == "scanner" || *role == "both" {
		go scanLoop(ctx)
	}

	if *role == "server" || *role == "both" {
		startServer(ctx)
	} else {
		<-ctx.Done()
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Pre-compiled regexes for ARP and NetBIOS parsing
var (
	reArpEntry = regexp.MustCompile(`\((\d+\.\d+\.\d+\.\d+)\)\s+at\s+([0-9a-fA-F]{1,2}:[0-9a-fA-F]{1,2}:[0-9a-fA-F]{1,2}:[0-9a-fA-F]{1,2}:[0-9a-fA-F]{1,2}:[0-9a-fA-F]{1,2})`)
	reNetBIOS  = regexp.MustCompile(`(\S+)<00>`)
)

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

// --- Ping sweep ---

// pingSweep sends concurrent UDP probes to all IPs in the subnet,
// triggering ARP resolution so that `arp -a` can discover them.
func pingSweep(subnetStr string) {
	_, ipNet, err := net.ParseCIDR(subnetStr)
	if err != nil {
		log.Printf("Invalid subnet for ping sweep: %s", subnetStr)
		return
	}
	ones, _ := ipNet.Mask.Size()
	if ones < 20 {
		log.Printf("Subnet too large for ping sweep (%s, /%d), skipping", subnetStr, ones)
		return
	}

	baseIP := ipNet.IP.Mask(ipNet.Mask).To4()
	if baseIP == nil {
		return
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 100)

	ip := make(net.IP, 4)
	copy(ip, baseIP)
	for ; ipNet.Contains(ip); incIP(ip) {
		target := ip.String()
		wg.Add(1)
		sem <- struct{}{}
		go func(t string) {
			defer wg.Done()
			defer func() { <-sem }()
			conn, err := net.DialTimeout("udp", net.JoinHostPort(t, "9"), 200*time.Millisecond)
			if err == nil {
				conn.Write([]byte{0})
				conn.Close()
			}
		}(target)
	}
	wg.Wait()
}

func incIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

// --- ARP table ---

func parseArpTable() []Device {
	var devices []Device
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "arp", "-an").Output()
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
		// Skip multicast MACs (01-00-5E-xx prefix = IPv4 multicast, 33-33-xx = IPv6 multicast)
		if strings.HasPrefix(mac, "01-00-5E-") || strings.HasPrefix(mac, "33-33-") {
			continue
		}
		dev := Device{MAC: mac, IP: ip}
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

// --- NetBIOS ---

// scanNetBIOS queries NetBIOS names for a list of IPs via nmblookup (concurrent with timeout).
func scanNetBIOS(ips []string) map[string]string {
	result := make(map[string]string)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 10)

	for _, ip := range ips {
		wg.Add(1)
		sem <- struct{}{}
		go func(ip string) {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, "nmblookup", "-A", ip).Output()
			if err != nil {
				return
			}
			matches := reNetBIOS.FindStringSubmatch(string(output))
			if len(matches) == 2 && len(matches[1]) < 16 {
				mu.Lock()
				result[ip] = matches[1]
				mu.Unlock()
			}
		}(ip)
	}
	wg.Wait()
	return result
}

// --- Main scan orchestrator ---

func scanNetwork(subnetStr string) []Device {
	// Step 1: Ping sweep to populate ARP table
	log.Printf("Ping sweep %s ...", subnetStr)
	pingSweep(subnetStr)

	// Step 2: Read ARP table
	devices := parseArpTable()

	// Collect IPs
	var allIPs []string
	var nbIPs []string
	for _, d := range devices {
		allIPs = append(allIPs, d.IP)
		if d.Name == "" {
			nbIPs = append(nbIPs, d.IP)
		}
	}

	// Step 3: Concurrent auxiliary scans
	var wg sync.WaitGroup
	var nbNames map[string]string
	var dnsNames map[string]string

	// NetBIOS
	wg.Add(1)
	go func() {
		defer wg.Done()
		if len(nbIPs) > 0 {
			nbNames = scanNetBIOS(nbIPs)
		}
	}()

	// mDNS service discovery
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanMDNS()
	}()

	// DNS PTR reverse lookup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if len(allIPs) > 0 {
			dnsNames = lookupDNS(allIPs)
		}
	}()

	wg.Wait()

	// Step 4: Apply NetBIOS names
	for i := range devices {
		if name, ok := nbNames[devices[i].IP]; ok && devices[i].Name == "" {
			devices[i].Name = name
		}
	}

	// Step 5: Apply DNS hostnames
	for i := range devices {
		if hostname, ok := dnsNames[devices[i].IP]; ok {
			devices[i].Hostname = hostname
		}
	}

	return devices
}

// --- Scan loop ---

func reportToCentral() {
	if *central == "" {
		return
	}
	// Send classified device data from deviceStore (not raw scan results)
	mu.RLock()
	var devices []Device
	for _, dev := range deviceStore {
		if dev.Home == *homeName && dev.Online {
			devices = append(devices, *dev)
		}
	}
	mu.RUnlock()

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

	runScan(subnetStr)

	ticker := time.NewTicker(time.Duration(*interval) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runScan(subnetStr)
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

func runScan(subnetStr string) {
	log.Printf("Scanning %s...", *homeName)

	devices := scanNetwork(subnetStr)
	log.Printf("Found %d devices", len(devices))

	markAllOffline(*homeName)

	for _, d := range devices {
		// Get mDNS results before acquiring the main lock
		mdnsResults := getMDNSResults(d.IP)

		mu.Lock()
		dev := getOrCreateDeviceLocked(d.MAC, d.IP, *homeName)
		// Apply hostname from DNS/mDNS
		if d.Hostname != "" {
			dev.Hostname = d.Hostname
		}
		// Apply name from NetBIOS (only if not already set)
		if d.Name != "" && dev.Name == "" {
			dev.Name = d.Name
		}
		// Run classification engine
		classifyDevice(dev, mdnsResults)
		mu.Unlock()

		if dev.Vendor != "" || dev.Category != "" {
			log.Printf("  %s %s [%s %s]", d.IP, d.MAC, dev.Vendor, dev.Category)
		}
	}

	updateStats()
	saveDevices()
	reportToCentral()
}

package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DeviceCategory represents the type/category of a device.
type DeviceCategory string

const (
	CatPhone   DeviceCategory = "phone"
	CatLaptop  DeviceCategory = "laptop"
	CatDesktop DeviceCategory = "desktop"
	CatTablet  DeviceCategory = "tablet"
	CatTV      DeviceCategory = "tv"
	CatSpeaker DeviceCategory = "speaker"
	CatWatch   DeviceCategory = "watch"
	CatPrinter DeviceCategory = "printer"
	CatRouter  DeviceCategory = "router"
	CatNAS     DeviceCategory = "nas"
	CatIoT     DeviceCategory = "iot"
	CatUnknown DeviceCategory = "unknown"
)

// isValidCategory returns true if the string is a recognized device category.
func isValidCategory(cat string) bool {
	switch DeviceCategory(cat) {
	case CatPhone, CatLaptop, CatDesktop, CatTablet, CatTV, CatSpeaker,
		CatWatch, CatPrinter, CatRouter, CatNAS, CatIoT, CatUnknown:
		return true
	}
	return false
}

type Device struct {
	MAC            string         `json:"mac"`
	IP             string         `json:"ip"`
	Name           string         `json:"name,omitempty"`
	Owner          string         `json:"owner,omitempty"`
	Home           string         `json:"home"`
	Known          bool           `json:"known"`
	Online         bool           `json:"online"`
	LastSeen       int64          `json:"lastSeen"`
	DeviceType     string         `json:"deviceType,omitempty"`     // backward compat
	Vendor         string         `json:"vendor,omitempty"`         // OUI vendor name
	Category       DeviceCategory `json:"category,omitempty"`       // device category
	Hostname       string         `json:"hostname,omitempty"`       // mDNS/DNS hostname
	ManualCategory bool           `json:"manualCategory,omitempty"` // true = user-set category
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

// Global state (mutex-protected)
var (
	deviceStore = make(map[string]*Device)
	stats       = make(map[string]HomeStats)
	mu          sync.RWMutex
)

// normalizeMac converts any MAC format to uppercase dash-separated: "AA-BB-CC-DD-EE-FF"
// Handles short-form octets from macOS arp output (e.g., "f0:55:1:39:36:2e" → "F0-55-01-39-36-2E").
func normalizeMac(mac string) string {
	// Detect separator (colon or dash with 6 octets)
	var sep string
	if strings.Contains(mac, ":") {
		sep = ":"
	} else if strings.Contains(mac, "-") {
		sep = "-"
	}
	if sep != "" {
		octets := strings.Split(mac, sep)
		if len(octets) == 6 {
			parts := make([]string, 6)
			for i, octet := range octets {
				o := strings.ToUpper(octet)
				if len(o) == 1 {
					o = "0" + o
				}
				parts[i] = o
			}
			return strings.Join(parts, "-")
		}
	}
	// Dot-separated (e.g., "AABB.CCDD.EEFF") or no separator
	raw := strings.ToUpper(strings.NewReplacer(":", "", "-", "", ".", "").Replace(mac))
	if len(raw) != 12 {
		return strings.ToUpper(mac)
	}
	parts := make([]string, 6)
	for i := 0; i < 6; i++ {
		parts[i] = raw[i*2 : i*2+2]
	}
	return strings.Join(parts, "-")
}

// rawMAC returns the normalized MAC without separators: "AA-BB-CC-DD-EE-FF" → "AABBCCDDEEFF"
func rawMAC(mac string) string {
	return strings.ReplaceAll(normalizeMac(mac), "-", "")
}

// initDevice creates or updates a device in the store. Returns the device pointer.
// Caller must NOT hold mu.
func initDevice(mac, ip, home string) *Device {
	mac = normalizeMac(mac)
	mu.Lock()
	defer mu.Unlock()
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

// getOrCreateDeviceLocked is like initDevice but requires mu to be held by the caller.
func getOrCreateDeviceLocked(mac, ip, home string) *Device {
	mac = normalizeMac(mac)
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
		return
	}
	migrateDevices()
}

// migrateDevices converts old DeviceType values to Vendor + Category.
func migrateDevices() {
	for _, dev := range deviceStore {
		if dev.Vendor != "" || dev.Category != "" {
			continue
		}
		if dev.DeviceType == "" {
			continue
		}
		dt := strings.ToLower(dev.DeviceType)
		switch {
		case strings.Contains(dt, "iphone"):
			dev.Vendor = "Apple, Inc."
			dev.Category = CatPhone
		case strings.Contains(dt, "macbook"):
			dev.Vendor = "Apple, Inc."
			dev.Category = CatLaptop
		case strings.Contains(dt, "ipad"):
			dev.Vendor = "Apple, Inc."
			dev.Category = CatTablet
		case strings.Contains(dt, "apple tv"):
			dev.Vendor = "Apple, Inc."
			dev.Category = CatTV
		case strings.Contains(dt, "watch"):
			dev.Vendor = "Apple, Inc."
			dev.Category = CatWatch
		case strings.Contains(dt, "mac"):
			dev.Vendor = "Apple, Inc."
			dev.Category = CatDesktop
		case strings.Contains(dt, "apple"):
			dev.Vendor = "Apple, Inc."
		case strings.Contains(dt, "samsung"):
			dev.Vendor = "Samsung Electronics Co.,Ltd"
		}
	}
}

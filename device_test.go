package main

import (
	"encoding/json"
	"testing"
)

func TestRawMAC(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"aa:bb:cc:dd:ee:ff", "AABBCCDDEEFF"},
		{"AA:BB:CC:DD:EE:FF", "AABBCCDDEEFF"},
		{"AA-BB-CC-DD-EE-FF", "AABBCCDDEEFF"},
		{"aabb.ccdd.eeff", "AABBCCDDEEFF"},
		// Short-form MACs from macOS arp (e.g., "1:0:5e:0:0:fb")
		{"f0:55:1:39:36:2e", "F0550139362E"},
		{"1:0:5e:0:0:fb", "01005E0000FB"},
		{"", ""},
	}
	for _, tt := range tests {
		got := rawMAC(tt.input)
		if got != tt.want {
			t.Errorf("rawMAC(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeMac(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"aa:bb:cc:dd:ee:ff", "AA-BB-CC-DD-EE-FF"},
		{"AA:BB:CC:DD:EE:FF", "AA-BB-CC-DD-EE-FF"},
		{"AA-BB-CC-DD-EE-FF", "AA-BB-CC-DD-EE-FF"},
		{"aabbccddeeff", "AA-BB-CC-DD-EE-FF"},
		{"AABBCCDDEEFF", "AA-BB-CC-DD-EE-FF"},
		// Dot-separated (Cisco format)
		{"aabb.ccdd.eeff", "AA-BB-CC-DD-EE-FF"},
		// Short-form MACs from macOS arp (single hex digits)
		{"f0:55:1:39:36:2e", "F0-55-01-39-36-2E"},
		{"1:0:5e:0:0:fb", "01-00-5E-00-00-FB"},
		{"50:a0:9:e7:6c:f9", "50-A0-09-E7-6C-F9"},
		{"7e:82:fa:b6:6:e0", "7E-82-FA-B6-06-E0"},
		// Short/invalid MAC: returned uppercased as-is
		{"abc", "ABC"},
		{"", ""},
	}
	for _, tt := range tests {
		got := normalizeMac(tt.input)
		if got != tt.want {
			t.Errorf("normalizeMac(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsValidCategory(t *testing.T) {
	valid := []string{
		"phone", "laptop", "desktop", "tablet", "tv", "speaker",
		"watch", "printer", "router", "nas", "iot", "unknown",
	}
	for _, cat := range valid {
		if !isValidCategory(cat) {
			t.Errorf("isValidCategory(%q) = false, want true", cat)
		}
	}

	invalid := []string{"", "smartphone", "PHONE", "Phone", "<script>", "computer"}
	for _, cat := range invalid {
		if isValidCategory(cat) {
			t.Errorf("isValidCategory(%q) = true, want false", cat)
		}
	}
}

func TestMigrateDevices(t *testing.T) {
	// Save and restore global state
	origStore := deviceStore
	defer func() { deviceStore = origStore }()

	deviceStore = map[string]*Device{
		"AA-BB-CC-DD-EE-01": {MAC: "AA-BB-CC-DD-EE-01", DeviceType: "Apple iPhone"},
		"AA-BB-CC-DD-EE-02": {MAC: "AA-BB-CC-DD-EE-02", DeviceType: "Apple MacBook"},
		"AA-BB-CC-DD-EE-03": {MAC: "AA-BB-CC-DD-EE-03", DeviceType: "Samsung"},
		"AA-BB-CC-DD-EE-04": {MAC: "AA-BB-CC-DD-EE-04", DeviceType: "Apple iPad/Mac"},
		"AA-BB-CC-DD-EE-05": {MAC: "AA-BB-CC-DD-EE-05", DeviceType: "Apple TV"},
		"AA-BB-CC-DD-EE-06": {MAC: "AA-BB-CC-DD-EE-06", DeviceType: "Apple Watch"},
		"AA-BB-CC-DD-EE-07": {MAC: "AA-BB-CC-DD-EE-07", DeviceType: "Apple Mac"},
		"AA-BB-CC-DD-EE-08": {MAC: "AA-BB-CC-DD-EE-08", DeviceType: ""},       // no type
		"AA-BB-CC-DD-EE-09": {MAC: "AA-BB-CC-DD-EE-09", Vendor: "Already Set"}, // already migrated
	}

	migrateDevices()

	tests := []struct {
		mac      string
		vendor   string
		category DeviceCategory
	}{
		{"AA-BB-CC-DD-EE-01", "Apple, Inc.", CatPhone},
		{"AA-BB-CC-DD-EE-02", "Apple, Inc.", CatLaptop},
		{"AA-BB-CC-DD-EE-03", "Samsung Electronics Co.,Ltd", ""},
		{"AA-BB-CC-DD-EE-04", "Apple, Inc.", CatTablet}, // "ipad" matched before "mac"
		{"AA-BB-CC-DD-EE-05", "Apple, Inc.", CatTV},
		{"AA-BB-CC-DD-EE-06", "Apple, Inc.", CatWatch},
		{"AA-BB-CC-DD-EE-07", "Apple, Inc.", CatDesktop}, // "mac" without iphone/macbook/ipad
		{"AA-BB-CC-DD-EE-08", "", ""},                    // no DeviceType, no migration
		{"AA-BB-CC-DD-EE-09", "Already Set", ""},         // already migrated, skip
	}
	for _, tt := range tests {
		dev := deviceStore[tt.mac]
		if dev.Vendor != tt.vendor {
			t.Errorf("migrate %s: vendor = %q, want %q", tt.mac, dev.Vendor, tt.vendor)
		}
		if dev.Category != tt.category {
			t.Errorf("migrate %s: category = %q, want %q", tt.mac, dev.Category, tt.category)
		}
	}
}

func TestInitDevice_NewAndUpdate(t *testing.T) {
	origStore := deviceStore
	defer func() { deviceStore = origStore }()
	deviceStore = make(map[string]*Device)

	// Create new device
	dev := initDevice("aa:bb:cc:dd:ee:ff", "192.168.1.10", "TestHome")
	if dev.MAC != "AA-BB-CC-DD-EE-FF" {
		t.Errorf("MAC = %q, want %q", dev.MAC, "AA-BB-CC-DD-EE-FF")
	}
	if dev.IP != "192.168.1.10" {
		t.Errorf("IP = %q, want %q", dev.IP, "192.168.1.10")
	}
	if !dev.Online {
		t.Error("new device should be online")
	}
	if dev.Home != "TestHome" {
		t.Errorf("Home = %q, want %q", dev.Home, "TestHome")
	}

	// Update existing device
	dev.Online = false
	dev2 := initDevice("AA-BB-CC-DD-EE-FF", "192.168.1.20", "TestHome")
	if dev2 != dev {
		t.Error("should return the same pointer")
	}
	if !dev.Online {
		t.Error("updated device should be online")
	}
	if dev.IP != "192.168.1.20" {
		t.Errorf("updated IP = %q, want %q", dev.IP, "192.168.1.20")
	}
}

func TestGetOrCreateDeviceLocked(t *testing.T) {
	origStore := deviceStore
	defer func() { deviceStore = origStore }()
	deviceStore = make(map[string]*Device)

	mu.Lock()
	dev := getOrCreateDeviceLocked("aa:bb:cc:11:22:33", "10.0.0.1", "Home")
	mu.Unlock()

	if dev.MAC != "AA-BB-CC-11-22-33" {
		t.Errorf("MAC = %q, want %q", dev.MAC, "AA-BB-CC-11-22-33")
	}

	// Update existing
	dev.Online = false
	mu.Lock()
	dev2 := getOrCreateDeviceLocked("AA-BB-CC-11-22-33", "10.0.0.2", "Home")
	mu.Unlock()

	if dev2 != dev {
		t.Error("should return same pointer")
	}
	if !dev.Online {
		t.Error("should be set online")
	}
}

func TestUpdateStats(t *testing.T) {
	origStore := deviceStore
	origStats := stats
	defer func() { deviceStore = origStore; stats = origStats }()

	deviceStore = map[string]*Device{
		"A": {Home: "H1", Online: true, Known: true},
		"B": {Home: "H1", Online: true, Known: false},
		"C": {Home: "H1", Online: false, Known: true},
		"D": {Home: "H2", Online: true, Known: true},
	}

	updateStats()

	h1 := stats["H1"]
	if h1.Total != 3 {
		t.Errorf("H1 Total = %d, want 3", h1.Total)
	}
	if h1.Online != 2 {
		t.Errorf("H1 Online = %d, want 2", h1.Online)
	}
	if h1.Known != 1 {
		t.Errorf("H1 Known = %d, want 1", h1.Known)
	}

	h2 := stats["H2"]
	if h2.Total != 1 || h2.Online != 1 || h2.Known != 1 {
		t.Errorf("H2 = %+v, want {1,1,1}", h2)
	}
}

func TestDeviceJSON_BackwardCompat(t *testing.T) {
	// Verify that old JSON without new fields can be deserialized
	oldJSON := `{
		"AA-BB-CC-DD-EE-FF": {
			"mac": "AA-BB-CC-DD-EE-FF",
			"ip": "192.168.1.5",
			"name": "MyPhone",
			"home": "Home",
			"known": true,
			"online": true,
			"lastSeen": 1700000000000,
			"deviceType": "Apple iPhone"
		}
	}`

	var store map[string]*Device
	if err := json.Unmarshal([]byte(oldJSON), &store); err != nil {
		t.Fatalf("unmarshal old JSON: %v", err)
	}

	dev := store["AA-BB-CC-DD-EE-FF"]
	if dev == nil {
		t.Fatal("device not found after unmarshal")
	}
	if dev.Name != "MyPhone" {
		t.Errorf("Name = %q, want %q", dev.Name, "MyPhone")
	}
	if dev.DeviceType != "Apple iPhone" {
		t.Errorf("DeviceType = %q, want %q", dev.DeviceType, "Apple iPhone")
	}
	// New fields should be zero values
	if dev.Vendor != "" {
		t.Errorf("Vendor = %q, want empty", dev.Vendor)
	}
	if dev.Category != "" {
		t.Errorf("Category = %q, want empty", dev.Category)
	}
	if dev.ManualCategory {
		t.Error("ManualCategory should be false")
	}
}

package main

import (
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Initialize OUI database before running tests
	initOUI()
	m.Run()
}

func TestInitOUI(t *testing.T) {
	if len(ouiDB) < 30000 {
		t.Errorf("OUI database too small: %d entries (expected 30000+)", len(ouiDB))
	}
}

func TestLookupVendor(t *testing.T) {
	tests := []struct {
		mac  string
		want string
	}{
		// Apple OUI prefix: F0-EE-7A maps to "Apple, Inc." in IEEE OUI
		{"F0-EE-7A-00-11-22", "Apple, Inc."},
		// 00-00-00 is assigned to XEROX CORPORATION in IEEE OUI
		{"00-00-00-00-00-00", "XEROX CORPORATION"},
		// Use a MAC prefix that is truly unassigned (FF-FF-FF is broadcast, not in OUI)
		{"FF-FF-FF-00-00-00", ""},
		// Invalid MAC
		{"xyz", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := lookupVendor(tt.mac)
		if got != tt.want {
			t.Errorf("lookupVendor(%q) = %q, want %q", tt.mac, got, tt.want)
		}
	}
}

func TestLookupVendor_KnownPrefixes(t *testing.T) {
	// Verify some well-known vendors exist in the database
	knownPrefixes := map[string]string{
		"F0EE7A": "Apple, Inc.",
	}
	for prefix, expectedVendor := range knownPrefixes {
		if vendor, ok := ouiDB[prefix]; ok {
			if vendor != expectedVendor {
				t.Errorf("ouiDB[%q] = %q, want %q", prefix, vendor, expectedVendor)
			}
		} else {
			t.Errorf("ouiDB[%q] not found, expected %q", prefix, expectedVendor)
		}
	}
}

func TestShortVendor(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Apple, Inc.", "Apple"},
		{"Samsung Electronics Co.,Ltd", "Samsung"},
		{"Google, Inc.", "Google"},
		{"TP-LINK TECHNOLOGIES CO.,LTD.", "TP-Link"},
		{"NETGEAR", "NETGEAR"},
		{"Synology Incorporated", "Synology"},
		{"HP Inc.", "HP"},
		{"ASUSTek COMPUTER INC.", "ASUS"},
		// Fallback: split on comma
		{"SomeVendor, LLC", "SomeVendor"},
		// Fallback: split on period
		{"Acme Corp. Ltd", "Acme Corp"},
		// Short unknown vendor: return as-is
		{"TinyVendor", "TinyVendor"},
		// Very long unknown vendor: truncated to 20 chars
		{"VeryLongVendorNameThatExceeds20Characters", "VeryLongVendorNameTh"},
	}
	for _, tt := range tests {
		got := shortVendor(tt.input)
		if got != tt.want {
			t.Errorf("shortVendor(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCategoryDisplayName(t *testing.T) {
	tests := []struct {
		cat  DeviceCategory
		want string
	}{
		{CatPhone, "Phone"},
		{CatLaptop, "Laptop"},
		{CatTV, "TV"},
		{CatUnknown, "Unknown"},
		{DeviceCategory("custom"), "custom"}, // unknown category returns raw string
	}
	for _, tt := range tests {
		got := categoryDisplayName(tt.cat)
		if got != tt.want {
			t.Errorf("categoryDisplayName(%q) = %q, want %q", tt.cat, got, tt.want)
		}
	}
}

func TestClassifyDevice_HostnamePatterns(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		wantCat  DeviceCategory
		wantVend string
	}{
		{"iPhone hostname", "Johns-iPhone", CatPhone, "Apple"},
		{"iPad hostname", "iPad-Air", CatTablet, "Apple"},
		{"MacBook hostname", "Sarahs-MacBook-Pro", CatLaptop, "Apple"},
		{"iMac hostname", "office-imac", CatDesktop, "Apple"},
		{"AppleTV hostname", "Living-Room-Apple-TV", CatTV, "Apple"},
		{"HomePod hostname", "Kitchen-HomePod", CatSpeaker, "Apple"},
		{"Galaxy hostname", "Galaxy-S24", CatPhone, "Samsung"},
		{"Pixel hostname", "Pixel-8", CatPhone, "Google"},
		{"Chromecast hostname", "Living-Chromecast", CatTV, "Google"},
		{"Xbox hostname", "Xbox-Series-X", CatTV, "Microsoft"},
		{"Printer hostname", "Office-Printer-01", CatPrinter, ""},
		{"NAS hostname", "my-nas-server", CatNAS, ""},
		{"Router hostname", "home-router", CatRouter, ""},
		{"No match", "random-device-42", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use a MAC with no OUI match so hostname vendor override works
			dev := &Device{MAC: "FF-00-00-00-00-00", Hostname: tt.hostname}
			classifyDevice(dev, nil)

			if dev.Category != tt.wantCat {
				t.Errorf("category = %q, want %q", dev.Category, tt.wantCat)
			}
			if tt.wantVend != "" && dev.Vendor != tt.wantVend {
				t.Errorf("vendor = %q, want %q", dev.Vendor, tt.wantVend)
			}
		})
	}
}

func TestClassifyDevice_VendorHints(t *testing.T) {
	tests := []struct {
		name    string
		vendor  string
		wantCat DeviceCategory
	}{
		{"Sonos speaker", "Sonos, Inc.", CatSpeaker},
		{"Roku TV", "Roku, Inc.", CatTV},
		{"Synology NAS", "Synology Incorporated", CatNAS},
		{"Raspberry Pi IoT", "Raspberry Pi Ltd", CatIoT},
		{"Canon printer", "CANON INC.", CatPrinter},
		{"Apple - no hint (multi-product)", "Apple, Inc.", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dev := &Device{MAC: "00-00-00-00-00-00", Vendor: tt.vendor}
			classifyDevice(dev, nil)

			if dev.Category != tt.wantCat {
				t.Errorf("category = %q, want %q", dev.Category, tt.wantCat)
			}
		})
	}
}

func TestClassifyDevice_MDNSPriority(t *testing.T) {
	// mDNS should override hostname pattern when priority is higher
	dev := &Device{
		MAC:      "00-00-00-00-00-00",
		Hostname: "Johns-MacBook-Pro", // hostname says laptop (priority 80)
	}
	mdns := []mdnsCacheEntry{
		{Category: CatPrinter, Priority: 95, Hostname: "printer.local", Expires: time.Now().Add(5 * time.Minute)},
	}

	classifyDevice(dev, mdns)

	if dev.Category != CatPrinter {
		t.Errorf("category = %q, want %q (mDNS priority 95 > hostname priority 80)", dev.Category, CatPrinter)
	}
}

func TestClassifyDevice_HostnameOverridesMDNS(t *testing.T) {
	// When hostname has higher effective priority than mDNS
	dev := &Device{
		MAC:      "00-00-00-00-00-00",
		Hostname: "Johns-iPhone",
	}
	mdns := []mdnsCacheEntry{
		{Category: CatDesktop, Priority: 40, Hostname: "ssh.local", Expires: time.Now().Add(5 * time.Minute)}, // SSH = desktop, priority 40
	}

	classifyDevice(dev, mdns)

	if dev.Category != CatPhone {
		t.Errorf("category = %q, want %q (hostname priority 80 > mDNS priority 40)", dev.Category, CatPhone)
	}
}

func TestClassifyDevice_ManualCategoryNotOverwritten(t *testing.T) {
	dev := &Device{
		MAC:            "00-00-00-00-00-00",
		Category:       CatTV,
		ManualCategory: true,
		Hostname:       "Johns-iPhone", // would normally classify as phone
	}

	classifyDevice(dev, nil)

	if dev.Category != CatTV {
		t.Errorf("manual category should not be overwritten: got %q, want %q", dev.Category, CatTV)
	}
}

func TestClassifyDevice_UpdatesDeviceType(t *testing.T) {
	dev := &Device{
		MAC:    "00-00-00-00-00-00",
		Vendor: "Apple, Inc.",
	}
	mdns := []mdnsCacheEntry{
		{Category: CatPhone, Priority: 90, Expires: time.Now().Add(5 * time.Minute)},
	}

	classifyDevice(dev, mdns)

	if dev.DeviceType != "Apple Phone" {
		t.Errorf("DeviceType = %q, want %q", dev.DeviceType, "Apple Phone")
	}
}

func TestClassifyDevice_MDNSHostnameFallback(t *testing.T) {
	dev := &Device{
		MAC: "00-00-00-00-00-00",
		// No hostname set
	}
	mdns := []mdnsCacheEntry{
		{Category: CatSpeaker, Priority: 75, Hostname: "sonos-beam.local", Expires: time.Now().Add(5 * time.Minute)},
	}

	classifyDevice(dev, mdns)

	if dev.Hostname != "sonos-beam.local" {
		t.Errorf("hostname should be set from mDNS: got %q", dev.Hostname)
	}
}

func TestHostnamePattern_SwitchNotTooGreedy(t *testing.T) {
	// "switch" should NOT match network switches
	dev := &Device{MAC: "00-00-00-00-00-00", Hostname: "core-switch-01"}
	classifyDevice(dev, nil)

	if dev.Category == CatTV {
		t.Error("network switch should not be classified as TV")
	}
}

func TestHostnamePattern_CaseInsensitive(t *testing.T) {
	cases := []struct {
		hostname string
		wantCat  DeviceCategory
	}{
		{"IPHONE", CatPhone},
		{"iphone", CatPhone},
		{"IPhone", CatPhone},
		{"MACBOOK", CatLaptop},
		{"MacBook", CatLaptop},
	}
	for _, tt := range cases {
		dev := &Device{MAC: "00-00-00-00-00-00", Hostname: tt.hostname}
		classifyDevice(dev, nil)
		if dev.Category != tt.wantCat {
			t.Errorf("hostname %q: category = %q, want %q", tt.hostname, dev.Category, tt.wantCat)
		}
	}
}

func TestUpdateDeviceType(t *testing.T) {
	tests := []struct {
		name       string
		vendor     string
		category   DeviceCategory
		wantType   string
	}{
		{"vendor and category", "Apple, Inc.", CatPhone, "Apple Phone"},
		{"vendor only", "Samsung Electronics Co.,Ltd", "", "Samsung"},
		{"category only", "", CatRouter, "Router"},
		{"vendor with unknown category", "Apple, Inc.", CatUnknown, "Apple"},
		{"nothing", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dev := &Device{Vendor: tt.vendor, Category: tt.category}
			updateDeviceType(dev)
			if dev.DeviceType != tt.wantType {
				t.Errorf("DeviceType = %q, want %q", dev.DeviceType, tt.wantType)
			}
		})
	}
}

package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"log"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
)

//go:embed oui.txt
var ouiData []byte

// ouiDB maps 3-byte uppercase hex prefix (e.g., "AABBCC") to vendor name.
var ouiDB map[string]string

// reOUI parses IEEE OUI file lines like: "  AA-BB-CC   (hex)\t\tVendor Name"
var reOUI = regexp.MustCompile(`^\s*([0-9A-Fa-f]{2})-([0-9A-Fa-f]{2})-([0-9A-Fa-f]{2})\s+\(hex\)\s+(.+)$`)

func initOUI() {
	ouiDB = make(map[string]string, 40000)
	scanner := bufio.NewScanner(bytes.NewReader(ouiData))
	for scanner.Scan() {
		matches := reOUI.FindStringSubmatch(scanner.Text())
		if len(matches) == 5 {
			prefix := strings.ToUpper(matches[1] + matches[2] + matches[3])
			ouiDB[prefix] = strings.TrimSpace(matches[4])
		}
	}
	log.Printf("OUI database loaded: %d vendors", len(ouiDB))
}

// lookupVendor returns the vendor name for a MAC address.
func lookupVendor(mac string) string {
	raw := rawMAC(mac)
	if len(raw) >= 6 {
		if vendor, ok := ouiDB[raw[:6]]; ok {
			return vendor
		}
	}
	return ""
}

// vendorCategoryHints maps single-product-line vendors to device categories.
var vendorCategoryHints = map[string]DeviceCategory{
	"Sonos, Inc.":                  CatSpeaker,
	"Roku, Inc.":                   CatTV,
	"Roku, Inc":                    CatTV,
	"NETGEAR":                      CatRouter,
	"TP-LINK TECHNOLOGIES CO.,LTD.": CatRouter,
	"TP-Link Corporation Limited":  CatRouter,
	"Synology Incorporated":        CatNAS,
	"Raspberry Pi Foundation":      CatIoT,
	"Raspberry Pi (Trading) Ltd":   CatIoT,
	"Raspberry Pi Ltd":             CatIoT,
	"Brother Industries, LTD.":     CatPrinter,
	"Seiko Epson Corporation":      CatPrinter,
	"Ubiquiti Inc":                 CatRouter,
	"Ubiquiti Networks Inc.":       CatRouter,
	"CANON INC.":                   CatPrinter,
	"Nest Labs Inc.":               CatIoT,
	"Ring LLC":                     CatIoT,
	"ecobee inc":                   CatIoT,
	"Philips Lighting BV":          CatIoT,
	"Signify B.V.":                 CatIoT,
	"ARRIS Group, Inc.":            CatRouter,
	"Belkin International Inc.":    CatRouter,
	"Hikvision Digital Technology":  CatIoT,
}

// hostnamePatterns maps hostname keywords to device category.
var hostnamePatterns = []struct {
	Pattern  *regexp.Regexp
	Category DeviceCategory
	Vendor   string // optional: override vendor if non-empty
}{
	{regexp.MustCompile(`(?i)iphone`), CatPhone, "Apple"},
	{regexp.MustCompile(`(?i)ipad`), CatTablet, "Apple"},
	{regexp.MustCompile(`(?i)macbook`), CatLaptop, "Apple"},
	{regexp.MustCompile(`(?i)imac`), CatDesktop, "Apple"},
	{regexp.MustCompile(`(?i)mac-?pro`), CatDesktop, "Apple"},
	{regexp.MustCompile(`(?i)mac-?mini`), CatDesktop, "Apple"},
	{regexp.MustCompile(`(?i)mac-?studio`), CatDesktop, "Apple"},
	{regexp.MustCompile(`(?i)apple-?tv`), CatTV, "Apple"},
	{regexp.MustCompile(`(?i)homepod`), CatSpeaker, "Apple"},
	{regexp.MustCompile(`(?i)apple-?watch`), CatWatch, "Apple"},
	{regexp.MustCompile(`(?i)galaxy`), CatPhone, "Samsung"},
	{regexp.MustCompile(`(?i)pixel`), CatPhone, "Google"},
	{regexp.MustCompile(`(?i)chromecast`), CatTV, "Google"},
	{regexp.MustCompile(`(?i)google-?home`), CatSpeaker, "Google"},
	{regexp.MustCompile(`(?i)nest-?hub`), CatSpeaker, "Google"},
	{regexp.MustCompile(`(?i)echo\b`), CatSpeaker, "Amazon"},
	{regexp.MustCompile(`(?i)fire-?tv`), CatTV, "Amazon"},
	{regexp.MustCompile(`(?i)kindle`), CatTablet, "Amazon"},
	{regexp.MustCompile(`(?i)surface`), CatLaptop, "Microsoft"},
	{regexp.MustCompile(`(?i)xbox`), CatTV, "Microsoft"},
	{regexp.MustCompile(`(?i)playstation|ps[45]`), CatTV, "Sony"},
	{regexp.MustCompile(`(?i)\bnintendo\b`), CatTV, "Nintendo"},
	{regexp.MustCompile(`(?i)roku`), CatTV, "Roku"},
	{regexp.MustCompile(`(?i)printer|laserjet|officejet|mfc-`), CatPrinter, ""},
	{regexp.MustCompile(`(?i)nas\b|diskstation|synology`), CatNAS, ""},
	{regexp.MustCompile(`(?i)router|gateway`), CatRouter, ""},
	{regexp.MustCompile(`(?i)laptop|notebook`), CatLaptop, ""},
	{regexp.MustCompile(`(?i)desktop|workstation`), CatDesktop, ""},
	{regexp.MustCompile(`(?i)-tv\b|smart-?tv|bravia|lg-?tv`), CatTV, ""},
}

// classifySignal holds a single classification signal with its priority.
type classifySignal struct {
	Category DeviceCategory
	Vendor   string
	Priority int
}

// classifyDevice merges all signals to determine device vendor and category.
// Must be called with mu held. Does NOT overwrite user-manually-set categories.
func classifyDevice(dev *Device, mdnsResults []mdnsCacheEntry) {
	// Always update vendor from OUI if not set
	if dev.Vendor == "" {
		dev.Vendor = lookupVendor(dev.MAC)
	}

	if dev.ManualCategory {
		updateDeviceType(dev)
		return
	}

	var signals []classifySignal

	// Signal 1: mDNS results (highest priority)
	for _, entry := range mdnsResults {
		signals = append(signals, classifySignal{
			Category: entry.Category,
			Priority: entry.Priority,
		})
		// Use mDNS hostname if device doesn't have one
		if entry.Hostname != "" && dev.Hostname == "" {
			dev.Hostname = entry.Hostname
		}
	}

	// Signal 2: Hostname heuristics (priority 80)
	for _, hostname := range []string{dev.Hostname, dev.Name} {
		if hostname == "" {
			continue
		}
		for _, p := range hostnamePatterns {
			if p.Pattern.MatchString(hostname) {
				sig := classifySignal{Category: p.Category, Priority: 80}
				if p.Vendor != "" {
					sig.Vendor = p.Vendor
				}
				signals = append(signals, sig)
				break
			}
		}
	}

	// Signal 3: Vendor category hints (priority 50)
	if dev.Vendor != "" {
		if cat, ok := vendorCategoryHints[dev.Vendor]; ok {
			signals = append(signals, classifySignal{Category: cat, Priority: 50})
		}
	}

	// Pick highest priority signal
	var best classifySignal
	for _, s := range signals {
		if s.Priority > best.Priority {
			best = s
		}
	}

	if best.Category != "" {
		dev.Category = best.Category
	}
	if best.Vendor != "" && dev.Vendor == "" {
		dev.Vendor = best.Vendor
	}

	updateDeviceType(dev)
}

// updateDeviceType sets the backward-compatible DeviceType field from Vendor + Category.
func updateDeviceType(dev *Device) {
	if dev.Vendor != "" && dev.Category != "" && dev.Category != CatUnknown {
		dev.DeviceType = shortVendor(dev.Vendor) + " " + categoryDisplayName(dev.Category)
	} else if dev.Vendor != "" {
		dev.DeviceType = shortVendor(dev.Vendor)
	} else if dev.Category != "" && dev.Category != CatUnknown {
		dev.DeviceType = categoryDisplayName(dev.Category)
	}
}

// shortVendorMap maps full OUI vendor names to short display names.
var shortVendorMap = map[string]string{
	"Apple, Inc.":                     "Apple",
	"Samsung Electronics Co.,Ltd":     "Samsung",
	"Google, Inc.":                    "Google",
	"Amazon Technologies Inc.":        "Amazon",
	"Microsoft Corporation":           "Microsoft",
	"Sony Interactive Entertainment Inc.": "Sony",
	"Huawei Technologies Co.,Ltd":     "Huawei",
	"Xiaomi Communications Co Ltd":    "Xiaomi",
	"Intel Corporate":                 "Intel",
	"Raspberry Pi (Trading) Ltd":      "Raspberry Pi",
	"Raspberry Pi Ltd":                "Raspberry Pi",
	"TP-LINK TECHNOLOGIES CO.,LTD.":   "TP-Link",
	"TP-Link Corporation Limited":     "TP-Link",
	"NETGEAR":                         "NETGEAR",
	"Synology Incorporated":           "Synology",
	"Brother Industries, LTD.":        "Brother",
	"Seiko Epson Corporation":         "Epson",
	"CANON INC.":                      "Canon",
	"Sonos, Inc.":                     "Sonos",
	"Roku, Inc.":                      "Roku",
	"Roku, Inc":                       "Roku",
	"Dell Inc.":                       "Dell",
	"Lenovo":                          "Lenovo",
	"HP Inc.":                         "HP",
	"ASUSTek COMPUTER INC.":           "ASUS",
	"LG Electronics (Mobile Communications)": "LG",
	"OnePlus Technology (Shenzhen) Co., Ltd": "OnePlus",
}

// shortVendor extracts a clean short vendor name from the full OUI vendor string.
func shortVendor(vendor string) string {
	if short, ok := shortVendorMap[vendor]; ok {
		return short
	}
	// Fallback: take the first word/segment before comma or period
	for _, sep := range []string{",", ".", " Technologies", " Electronics", " Corporation", " International"} {
		if idx := strings.Index(vendor, sep); idx > 0 && idx < 20 {
			return strings.TrimSpace(vendor[:idx])
		}
	}
	if len(vendor) > 20 {
		return vendor[:20]
	}
	return vendor
}

var categoryNames = map[DeviceCategory]string{
	CatPhone:   "Phone",
	CatLaptop:  "Laptop",
	CatDesktop: "Desktop",
	CatTablet:  "Tablet",
	CatTV:      "TV",
	CatSpeaker: "Speaker",
	CatWatch:   "Watch",
	CatPrinter: "Printer",
	CatRouter:  "Router",
	CatNAS:     "NAS",
	CatIoT:     "IoT",
	CatUnknown: "Unknown",
}

func categoryDisplayName(cat DeviceCategory) string {
	if name, ok := categoryNames[cat]; ok {
		return name
	}
	return string(cat)
}

// --- DNS PTR reverse lookup ---

var (
	dnsCache   = make(map[string]dnsCacheEntry)
	dnsCacheMu sync.RWMutex
)

type dnsCacheEntry struct {
	Hostname string
	Expires  time.Time
}

// lookupDNS performs concurrent DNS PTR lookups for a list of IPs.
func lookupDNS(ips []string) map[string]string {
	result := make(map[string]string, len(ips))
	var uncached []string

	dnsCacheMu.RLock()
	now := time.Now()
	for _, ip := range ips {
		if entry, ok := dnsCache[ip]; ok && now.Before(entry.Expires) {
			if entry.Hostname != "" {
				result[ip] = entry.Hostname
			}
		} else {
			uncached = append(uncached, ip)
		}
	}
	dnsCacheMu.RUnlock()

	if len(uncached) == 0 {
		return result
	}

	var wg sync.WaitGroup
	var resMu sync.Mutex
	sem := make(chan struct{}, 20)

	for _, ip := range uncached {
		wg.Add(1)
		sem <- struct{}{}
		go func(ip string) {
			defer wg.Done()
			defer func() { <-sem }()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			names, err := net.DefaultResolver.LookupAddr(ctx, ip)
			hostname := ""
			if err == nil && len(names) > 0 {
				hostname = strings.TrimSuffix(names[0], ".")
			}

			dnsCacheMu.Lock()
			dnsCache[ip] = dnsCacheEntry{
				Hostname: hostname,
				Expires:  time.Now().Add(10 * time.Minute),
			}
			dnsCacheMu.Unlock()

			if hostname != "" {
				resMu.Lock()
				result[ip] = hostname
				resMu.Unlock()
			}
		}(ip)
	}
	wg.Wait()

	return result
}

package main

import (
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// mdnsServiceInfo defines the mapping from mDNS service type to device category.
type mdnsServiceInfo struct {
	Category DeviceCategory
	Priority int
}

var mdnsServiceMap = map[string]mdnsServiceInfo{
	"_companion-link._tcp": {CatPhone, 70},
	"_airplay._tcp":        {CatTV, 70},
	"_raop._tcp":           {CatSpeaker, 65},
	"_googlecast._tcp":     {CatTV, 80},
	"_ipp._tcp":            {CatPrinter, 95},
	"_printer._tcp":        {CatPrinter, 95},
	"_smb._tcp":            {CatNAS, 60},
	"_ssh._tcp":            {CatDesktop, 40},
	"_homekit._tcp":        {CatIoT, 50},
	"_spotify-connect._tcp": {CatSpeaker, 75},
}

type mdnsCacheEntry struct {
	IP       string
	Category DeviceCategory
	Priority int
	Hostname string
	Expires  time.Time
}

var (
	mdnsCache   = make(map[string][]mdnsCacheEntry) // keyed by IP
	mdnsCacheMu sync.RWMutex
)

// getMDNSResults returns cached mDNS results for the given IP.
func getMDNSResults(ip string) []mdnsCacheEntry {
	mdnsCacheMu.RLock()
	defer mdnsCacheMu.RUnlock()
	now := time.Now()
	var valid []mdnsCacheEntry
	for _, entry := range mdnsCache[ip] {
		if now.Before(entry.Expires) {
			valid = append(valid, entry)
		}
	}
	return valid
}

const mdnsAddr = "224.0.0.251:5353"

// scanMDNS queries known mDNS service types and updates the cache.
// Uses miekg/dns directly instead of zeroconf to avoid goroutine panic bugs.
func scanMDNS() {
	addr, err := net.ResolveUDPAddr("udp4", mdnsAddr)
	if err != nil {
		log.Printf("mDNS: resolve addr error: %v", err)
		return
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		log.Printf("mDNS: listen error: %v", err)
		return
	}
	defer conn.Close()

	var resultsMu sync.Mutex
	results := make(map[string][]mdnsCacheEntry)

	// Send PTR queries for all service types
	for service := range mdnsServiceMap {
		msg := new(dns.Msg)
		msg.SetQuestion(fmt.Sprintf("%s.local.", service), dns.TypePTR)
		msg.RecursionDesired = false
		buf, err := msg.Pack()
		if err != nil {
			continue
		}
		conn.WriteToUDP(buf, addr)
	}

	// Collect responses for 3 seconds
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 65536)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			break // timeout or error
		}

		var resp dns.Msg
		if err := resp.Unpack(buf[:n]); err != nil {
			continue
		}

		// Collect all records (answers + additional) for cross-referencing
		allRecords := append(resp.Answer, resp.Extra...)

		// Build hostname→IP and service→hostname maps from all records
		hostIPs := make(map[string][]string)
		for _, rr := range allRecords {
			if a, ok := rr.(*dns.A); ok {
				host := strings.TrimSuffix(a.Hdr.Name, ".")
				hostIPs[a.Hdr.Name] = append(hostIPs[a.Hdr.Name], a.A.String())
				hostIPs[host] = append(hostIPs[host], a.A.String())
			}
		}

		// Process PTR records to find service instances
		for _, rr := range resp.Answer {
			ptr, ok := rr.(*dns.PTR)
			if !ok {
				continue
			}

			// Extract service type from PTR name: "_service._tcp.local." → "_service._tcp"
			svcType := strings.TrimSuffix(ptr.Hdr.Name, ".local.")
			svcType = strings.TrimSuffix(svcType, ".")
			svcInfo, known := mdnsServiceMap[svcType]
			if !known {
				continue
			}

			// Find SRV record for this instance to get hostname
			instanceName := ptr.Ptr
			var hostname string
			for _, rr2 := range allRecords {
				if srv, ok := rr2.(*dns.SRV); ok && srv.Hdr.Name == instanceName {
					hostname = strings.TrimSuffix(srv.Target, ".")
					break
				}
			}

			// Resolve IPs from A records
			var ips []string
			if hostname != "" {
				ips = hostIPs[hostname+"."]
				if len(ips) == 0 {
					ips = hostIPs[hostname]
				}
			}

			// Create cache entries
			for _, ip := range ips {
				ce := mdnsCacheEntry{
					IP:       ip,
					Category: svcInfo.Category,
					Priority: svcInfo.Priority,
					Hostname: hostname,
					Expires:  time.Now().Add(5 * time.Minute),
				}
				resultsMu.Lock()
				results[ip] = append(results[ip], ce)
				resultsMu.Unlock()
			}
		}
	}

	// Merge results into cache
	mdnsCacheMu.Lock()
	now := time.Now()
	for ip, entries := range mdnsCache {
		var valid []mdnsCacheEntry
		for _, e := range entries {
			if now.Before(e.Expires) {
				valid = append(valid, e)
			}
		}
		if len(valid) > 0 {
			mdnsCache[ip] = valid
		} else {
			delete(mdnsCache, ip)
		}
	}
	for ip, entries := range results {
		mdnsCache[ip] = entries
	}
	mdnsCacheMu.Unlock()

	total := 0
	for _, entries := range results {
		total += len(entries)
	}
	if total > 0 {
		log.Printf("mDNS: found %d services on %d devices", total, len(results))
	}
}

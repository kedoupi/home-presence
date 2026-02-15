package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
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

// scanMDNS browses for known mDNS service types and updates the cache.
func scanMDNS() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		log.Printf("mDNS resolver init error: %v", err)
		return
	}

	var wg sync.WaitGroup
	var resultsMu sync.Mutex
	results := make(map[string][]mdnsCacheEntry)

	for service, info := range mdnsServiceMap {
		entries := make(chan *zeroconf.ServiceEntry, 32)
		svc := service
		svcInfo := info

		// Consumer: reads discovered entries
		wg.Add(1)
		go func() {
			defer wg.Done()
			for entry := range entries {
				for _, ip := range entry.AddrIPv4 {
					ipStr := ip.String()
					hostname := strings.TrimSuffix(entry.HostName, ".")
					ce := mdnsCacheEntry{
						IP:       ipStr,
						Category: svcInfo.Category,
						Priority: svcInfo.Priority,
						Hostname: hostname,
						Expires:  time.Now().Add(5 * time.Minute),
					}
					resultsMu.Lock()
					results[ipStr] = append(results[ipStr], ce)
					resultsMu.Unlock()
				}
			}
		}()

		// Producer: Browse blocks until ctx done.
		// Explicitly close channel after Browse returns as a safety net.
		go func() {
			_ = resolver.Browse(ctx, svc, "local.", entries)
			close(entries)
		}()
	}

	// Wait for context timeout
	<-ctx.Done()
	// Allow a brief moment for channels to flush
	time.Sleep(100 * time.Millisecond)
	// Wait for all consumers to finish
	wg.Wait()

	// Merge results into cache
	mdnsCacheMu.Lock()
	now := time.Now()
	// Clean expired entries
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
	// Add new results
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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

func startServer(ctx context.Context) {
	mux := http.NewServeMux()

	// POST /api/report — scanner nodes report devices
	mux.HandleFunc("/api/report", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var report DeviceReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		log.Printf("Received report from %s: %d devices", report.Home, len(report.Devices))
		markAllOffline(report.Home)
		for _, d := range report.Devices {
			mu.Lock()
			dev := getOrCreateDeviceLocked(d.MAC, d.IP, report.Home)
			if d.Hostname != "" {
				dev.Hostname = d.Hostname
			}
			if d.Name != "" && dev.Name == "" {
				dev.Name = d.Name
			}
			if d.Vendor != "" && dev.Vendor == "" {
				dev.Vendor = d.Vendor
			}
			if d.Category != "" && !dev.ManualCategory {
				dev.Category = d.Category
			}
			if d.DeviceType != "" {
				dev.DeviceType = d.DeviceType
			}
			mu.Unlock()
		}
		updateStats()
		saveDevices()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// GET /api/devices — list all devices + stats
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		devices := make([]Device, 0, len(deviceStore))
		for _, d := range deviceStore {
			dev := *d
			// Replace vendor with short display name for the frontend
			if dev.Vendor != "" {
				dev.Vendor = shortVendor(dev.Vendor)
			}
			devices = append(devices, dev)
		}
		s := stats
		mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"devices": devices, "stats": s})
	})

	// PUT /api/devices/:mac — update device name/owner/category
	mux.HandleFunc("/api/devices/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		mac := normalizeMac(strings.TrimPrefix(r.URL.Path, "/api/devices/"))
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Name     string `json:"name"`
			Owner    string `json:"owner"`
			Category string `json:"category,omitempty"`
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
		// Handle category: set manual override, or clear to auto-detect
		if req.Category != "" && isValidCategory(req.Category) {
			dev.Category = DeviceCategory(req.Category)
			dev.ManualCategory = true
			updateDeviceType(dev)
		} else if req.Category == "" {
			dev.ManualCategory = false
			// Category will be re-detected on next scan
		}
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

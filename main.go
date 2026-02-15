package main

import (
	"context"
	_ "embed"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
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

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime)

	if err := os.MkdirAll("data", 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	// Initialize OUI vendor database
	initOUI()

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

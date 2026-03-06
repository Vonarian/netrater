package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// ─── Tunable Configuration ──────────────────────────────────────────────────

const (
	// Network target
	TargetInterface = "wlx50ebf65e7306"
	TargetClass     = "1:1"
	PingHost        = "google.com"
	PingPort        = 443 // Set to 0 to use ICMP, or a port (e.g. 443) for TCPing

	// Bandwidth bounds (kbps)
	MinRate   = 1000
	MaxRate   = 7000
	StartRate = 3500

	// Pinger timing
	PingInterval  = 500 * time.Millisecond
	WindowSize    = 4 // rolling avg window (4 × 500ms = 2s)
	MinPingWindow = 60 * time.Second

	// Controller timing
	ControlInterval = 1 * time.Second

	// AIMD thresholds
	CongestionThresholdMs = 40.0 // ms above MinPing → congested
	ClearThresholdMs      = 15.0 // ms above MinPing → clear
	DecreaseMultiplier    = 0.85
	AdditiveIncrease      = 250 // kbps
)

// ─── Main ───────────────────────────────────────────────────────────────────

func main() {
	// No timestamp flags — journald adds its own timestamps.
	// This avoids double-stamped lines in `journalctl -u netrater`.
	log.SetFlags(0)
	log.Println("═══════════════════════════════════════════════════")
	log.Println("  NetRater – AIMD Bandwidth Controller")
	log.Printf("  Interface: %s  Class: %s  Ping: %s  Port: %d", TargetInterface, TargetClass, PingHost, PingPort)
	log.Printf("  Rate range: %d–%d kbps  Start: %d kbps", MinRate, MaxRate, StartRate)
	log.Println("═══════════════════════════════════════════════════")

	// Shared metrics between pinger and controller
	metrics := &PingerMetrics{}

	// Components
	pinger := NewPinger(PingHost, PingPort, PingInterval, WindowSize, MinPingWindow, metrics)
	executor := NewExecutor(TargetInterface, TargetClass)
	if err := executor.Setup(); err != nil {
		log.Fatalf("Failed to setup executor: %v", err)
	}
	controller := NewController(
		metrics, executor,
		ControlInterval,
		StartRate, MinRate, MaxRate,
		CongestionThresholdMs, ClearThresholdMs,
		DecreaseMultiplier, AdditiveIncrease,
	)

	// Stop channel for graceful shutdown
	stop := make(chan struct{})

	// Start goroutines
	go pinger.Run(stop)
	go controller.Run(stop)

	log.Println("[MAIN] Running. Press Ctrl+C to stop.")

	// Wait for termination signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	log.Printf("[MAIN] Received %v, shutting down...", sig)
	close(stop)

	// Give goroutines a moment to exit cleanly
	time.Sleep(200 * time.Millisecond)
	log.Println("[MAIN] Goodbye.")
}

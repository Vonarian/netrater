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

	// Proxy settings (e.g. "socks5://127.0.0.1:10808" or "" for none)
	ProxyAddress = "socks5://127.0.0.1:10808"

	// Bandwidth bounds (kbps)
	MinRate   = 1000
	MaxRate   = 7000
	StartRate = 3500
)

// Rotating Generate 204 URLs for realistic latency measurement
var PingURLs = []string{
	"http://connectivitycheck.gstatic.com/generate_204",
	"http://clients3.google.com/generate_204",
	"http://www.gstatic.com/generate_204",
	"http://connectivitycheck.android.com/generate_204",
	"http://play.googleapis.com/generate_204",
}

const (
	// Pinger timing
	PingInterval  = 500 * time.Millisecond
	WindowSize    = 4 // rolling avg window (4 × 500ms = 2s)
	MinPingWindow = 60 * time.Second

	// Controller timing
	ControlInterval = 1 * time.Second

	// AIMD thresholds
	// Since we are using a proxy, baseline latency is naturally high (250-400ms).
	// We only want to throttle if it spikes significantly above the base or exceeds 600ms.
	CongestionThresholdMs = 150.0 // ms above MinPing → congested
	ClearThresholdMs      = 50.0  // ms above MinPing → clear
	AdditiveIncrease      = 250   // kbps

	// Absolute ceiling for latency before we MUST throttle, regardless of MinPing
	MaxAcceptableRTTMs = 675.0
	DecreaseMultiplier = 0.85
)

// ─── Main ───────────────────────────────────────────────────────────────────

func main() {
	// No timestamp flags — journald adds its own timestamps.
	// This avoids double-stamped lines in `journalctl -u netrater`.
	log.SetFlags(0)
	log.Println("═══════════════════════════════════════════════════")
	log.Println("  NetRater – AIMD Bandwidth Controller")
	log.Printf("  Interface: %s  Class: %s  URLs: %d rotating", TargetInterface, TargetClass, len(PingURLs))
	if ProxyAddress != "" {
		log.Printf("  Proxy: %s", ProxyAddress)
	}
	log.Printf("  Rate range: %d–%d kbps  Start: %d kbps", MinRate, MaxRate, StartRate)
	log.Println("═══════════════════════════════════════════════════")

	// Shared metrics between pinger and controller
	metrics := &PingerMetrics{}

	// Components
	pinger := NewPinger(PingURLs, ProxyAddress, PingInterval, WindowSize, MinPingWindow, metrics)
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
		MaxAcceptableRTTMs,
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

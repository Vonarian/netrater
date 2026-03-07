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
	PingInterval = 1250 * time.Millisecond
	WindowSize   = 5 // rolling avg window (5 × 750ms = 3.75s)
	// Controller timing
	// Removed ControlInterval because evaluation is now sequential with pinging

	// ─── Proportional Control Configuration ───

	// Absolute ceiling for latency. If average RTT exceeds this, we dynamically throttle.
	// If it is below this, we dynamically increase.
	MaxAcceptableRTTMs = 900.0

	// Maximum kbps to add per evaluation (approached when latency is near 0ms)
	MaxAdditiveIncrease = 250.0

	// Aggressiveness of bandwidth reduction when latency exceeds target (1.0 = aggressive, 0.1 = very mild)
	ThrottleFactor = 0.5

	// Simulated RTT for a failed probe (e.g., timeout) to dynamically penalize loss
	TimeoutPenaltyMs = 2000.0
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
	} else {
		log.Print("No ProxyAddress set")
	}
	log.Printf("  Rate range: %d–%d kbps  Start: %d kbps", MinRate, MaxRate, StartRate)
	log.Println("═══════════════════════════════════════════════════")

	// Shared metrics between pinger and controller
	metrics := &PingerMetrics{}

	// Components
	pinger := NewPinger(PingURLs, ProxyAddress, PingInterval, WindowSize, TimeoutPenaltyMs, metrics)
	executor := NewExecutor(TargetInterface, TargetClass)
	if err := executor.Setup(); err != nil {
		log.Fatalf("Failed to setup executor: %v", err)
	}
	controller := NewController(
		metrics, executor,
		StartRate, MinRate, MaxRate,
		MaxAdditiveIncrease, MaxAcceptableRTTMs,
		ThrottleFactor,
	)

	// Stop channel for graceful shutdown
	stop := make(chan struct{})

	log.Println("[MAIN] Running sequentially. Press Ctrl+C to stop.")

	// Wait for termination signal in a separate goroutine
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("[MAIN] Received %v, shutting down...", sig)
		close(stop)
	}()

	// 24-hour cycle for IP refresh
	refreshTicker := time.NewTicker(24 * time.Hour)
	defer refreshTicker.Stop()

	// Initial resolution and rate application
	pinger.ResolveIPs()
	controller.ClampAndApply()

	// SEQUENTIAL MAIN LOOP
	for {
		select {
		case <-stop:
			time.Sleep(200 * time.Millisecond) // Give goroutines a moment to exit cleanly
			log.Println("[MAIN] Goodbye.")
			return
		case <-refreshTicker.C:
			pinger.ResolveIPs()
		default:
			// 1. Ping
			pinger.MeasureAndRecord()

			// 2. Evaluate
			controller.Evaluate()

			// 3. Sleep until next ping interval
			// Using select with time.After so it responds to stop signals during sleep
			select {
			case <-stop:
			case <-time.After(PingInterval):
			}
		}
	}
}

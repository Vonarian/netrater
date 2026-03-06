package main

import (
	"log"
	"time"
)

// Controller runs the AIMD control loop.
type Controller struct {
	metrics            *PingerMetrics
	executor           *Executor
	interval           time.Duration
	currentRate        int     // kbps
	minRate            int     // kbps
	maxRate            int     // kbps
	congestionThreshMs float64 // ms above MinPing to trigger throttle
	clearThreshMs      float64 // ms above MinPing to declare clear
	decreaseMult       float64 // multiplicative decrease factor
	additiveInc        int     // kbps additive increase
}

func NewController(
	metrics *PingerMetrics,
	executor *Executor,
	interval time.Duration,
	startRate, minRate, maxRate int,
	congestionThreshMs, clearThreshMs float64,
	decreaseMult float64,
	additiveInc int,
) *Controller {
	return &Controller{
		metrics:            metrics,
		executor:           executor,
		interval:           interval,
		currentRate:        startRate,
		minRate:            minRate,
		maxRate:            maxRate,
		congestionThreshMs: congestionThreshMs,
		clearThreshMs:      clearThreshMs,
		decreaseMult:       decreaseMult,
		additiveInc:        additiveInc,
	}
}

// Run starts the control loop. Blocks until stop is closed.
func (c *Controller) Run(stop <-chan struct{}) {
	// Apply the starting rate immediately
	c.clampAndApply()

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			c.evaluate()
		}
	}
}

func (c *Controller) evaluate() {
	// Snapshot the pinger metrics
	c.metrics.Mu.Lock()
	avgPing := c.metrics.AvgPing
	minPing := c.metrics.MinPing
	lossRatio := c.metrics.LossRatio
	c.metrics.Mu.Unlock()

	avgMs := float64(avgPing) / float64(time.Millisecond)
	minMs := float64(minPing) / float64(time.Millisecond)
	prevRate := c.currentRate

	switch {
	// --- 100% packet loss: maximum backoff ---
	case lossRatio >= 1.0:
		c.currentRate = int(float64(c.currentRate) * c.decreaseMult)
		log.Printf("[THROTTLE] 100%% packet loss! Cutting bandwidth to %d kbps.", c.currentRate)

	// --- Congested: MD ---
	case avgMs > (minMs+c.congestionThreshMs) || lossRatio > 0:
		c.currentRate = int(float64(c.currentRate) * c.decreaseMult)
		log.Printf("[THROTTLE] Ping spiked to %.1fms (min=%.1fms, loss=%.0f%%). Cutting bandwidth to %d kbps.",
			avgMs, minMs, lossRatio*100, c.currentRate)

	// --- Clear: AI ---
	case avgMs <= (minMs+c.clearThreshMs) && lossRatio == 0:
		c.currentRate = c.currentRate + c.additiveInc
		log.Printf("[CLEAR] Ping stable at %.1fms (min=%.1fms). Pushing bandwidth to %d kbps.",
			avgMs, minMs, c.currentRate)

	// --- Maintenance: hold ---
	default:
		log.Printf("[MAINTAIN] Ping at %.1fms (min=%.1fms). Holding at %d kbps.", avgMs, minMs, c.currentRate)
	}

	c.currentRate = clamp(c.currentRate, c.minRate, c.maxRate)

	if c.currentRate != prevRate {
		c.clampAndApply()
	}
}

func (c *Controller) clampAndApply() {
	c.currentRate = clamp(c.currentRate, c.minRate, c.maxRate)
	if err := c.executor.Apply(c.currentRate); err != nil {
		log.Printf("[CONTROLLER] Failed to apply rate: %v", err)
	}
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

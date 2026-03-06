package main

import (
	"log"
	"time"
)

// Controller runs the AIMD control loop.
type Controller struct {
	metrics            *PingerMetrics
	executor           *Executor
	currentRate        int     // kbps
	minRate            int     // kbps
	maxRate            int     // kbps
	congestionThreshMs float64 // ms above MinPing to trigger throttle
	clearThreshMs      float64 // ms above MinPing to declare clear
	decreaseMult       float64 // multiplicative decrease factor
	additiveInc        int     // kbps additive increase
	maxAcceptableMs    float64 // absolute latency ceiling
	lastThrottle       time.Time
	maintainCount      int
}

func NewController(
	metrics *PingerMetrics,
	executor *Executor,
	startRate, minRate, maxRate int,
	congestionThreshMs, clearThreshMs float64,
	decreaseMult float64,
	additiveInc int,
	maxAcceptableMs float64,
) *Controller {
	return &Controller{
		metrics:            metrics,
		executor:           executor,
		currentRate:        startRate,
		minRate:            minRate,
		maxRate:            maxRate,
		congestionThreshMs: congestionThreshMs,
		clearThreshMs:      clearThreshMs,
		decreaseMult:       decreaseMult,
		additiveInc:        additiveInc,
		maxAcceptableMs:    maxAcceptableMs,
		maintainCount:      0,
	}
}

// Evaluate assesses the current network conditions based on pinger metrics
// and decides whether to throttle, maintain, or increase the rate.
func (c *Controller) Evaluate() {
	// Snapshot the pinger metrics
	c.metrics.Mu.Lock()
	avgPing := c.metrics.AvgPing
	minPing := c.metrics.MinPing
	lossRatio := c.metrics.LossRatio
	c.metrics.Mu.Unlock()

	avgMs := float64(avgPing) / float64(time.Millisecond)
	minMs := float64(minPing) / float64(time.Millisecond)
	prevRate := c.currentRate
	now := time.Now()

	// Cooldown: don't throttle more than once every 2 seconds
	cooldown := 2 * time.Second

	switch {
	// --- 100% packet loss: maximum backoff ---
	case lossRatio >= 1.0:
		if now.Sub(c.lastThrottle) < cooldown {
			return
		}
		newRate := int(float64(c.currentRate) * c.decreaseMult)
		newRate = clamp(newRate, c.minRate, c.maxRate)
		if newRate < c.currentRate {
			log.Printf("[THROTTLE] 100%% packet loss! Cutting bandwidth from %d to %d kbps.", c.currentRate, newRate)
			c.lastThrottle = now
		} else {
			log.Printf("[THROTTLE] 100%% packet loss! Holding at minimum rate %d kbps.", c.currentRate)
		}
		c.currentRate = newRate

	// --- Congested: MD (spiked or > absolute max) ---
	case avgMs > (minMs+c.congestionThreshMs) || avgMs > c.maxAcceptableMs || lossRatio > 0:
		if now.Sub(c.lastThrottle) < cooldown {
			return
		}
		newRate := int(float64(c.currentRate) * c.decreaseMult)
		newRate = clamp(newRate, c.minRate, c.maxRate)
		if newRate < c.currentRate {
			log.Printf("[THROTTLE] Ping spiked to %.1fms (min=%.1fms, loss=%.0f%%). Cutting bandwidth from %d to %d kbps.",
				avgMs, minMs, lossRatio*100, c.currentRate, newRate)
			c.lastThrottle = now
		} else {
			log.Printf("[THROTTLE] Ping spiked to %.1fms (min=%.1fms, loss=%.0f%%). Holding at minimum rate %d kbps.",
				avgMs, minMs, lossRatio*100, c.currentRate)
		}
		c.currentRate = newRate

	// --- Clear: AI ---
	case avgMs <= (minMs+c.clearThreshMs) && lossRatio == 0:
		newRate := c.currentRate + c.additiveInc
		newRate = clamp(newRate, c.minRate, c.maxRate)
		if newRate > c.currentRate {
			log.Printf("[CLEAR] Ping stable at %.1fms (min=%.1fms). Pushing bandwidth from %d to %d kbps.",
				avgMs, minMs, c.currentRate, newRate)
		} else {
			c.maintainCount++
			if c.maintainCount%10 == 0 {
				log.Printf("[MAINTAIN] Ping stable at %.1fms (min=%.1fms). Holding at maximum rate %d kbps.",
					avgMs, minMs, c.currentRate)
			}
		}
		c.currentRate = newRate

	// --- Maintenance: hold ---
	default:
		c.maintainCount++
		if c.maintainCount%10 == 0 {
			log.Printf("[MAINTAIN] Ping at %.1fms (min=%.1fms). Holding at %d kbps.", avgMs, minMs, c.currentRate)
		}
	}

	if c.currentRate != prevRate {
		c.ClampAndApply()
	}
}

// ClampAndApply enforces rate bounds and applies the current rate via the executor.
func (c *Controller) ClampAndApply() {
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

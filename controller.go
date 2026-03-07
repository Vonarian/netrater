package main

import (
	"log"
	"time"
)

// Controller runs the AIMD control loop.
type Controller struct {
	metrics         *PingerMetrics
	executor        *Executor
	currentRate     int     // kbps
	minRate         int     // kbps
	maxRate         int     // kbps
	maxAdditiveInc  float64 // max kbps additive increase
	maxAcceptableMs float64 // absolute latency ceiling
	throttleFactor  float64 // softens bandwidth cuts (1.0 = full, 0.5 = 50%)
	lastThrottle    time.Time
	maintainCount   int
}

func NewController(
	metrics *PingerMetrics,
	executor *Executor,
	startRate, minRate, maxRate int,
	maxAdditiveInc float64,
	maxAcceptableMs float64,
	throttleFactor float64,
) *Controller {
	return &Controller{
		metrics:         metrics,
		executor:        executor,
		currentRate:     startRate,
		minRate:         minRate,
		maxRate:         maxRate,
		maxAdditiveInc:  maxAdditiveInc,
		maxAcceptableMs: maxAcceptableMs,
		throttleFactor:  throttleFactor,
		maintainCount:   0,
	}
}

// Evaluate assesses the current network conditions based on pinger metrics
// and decides whether to throttle, maintain, or increase the rate.
func (c *Controller) Evaluate() {
	// Snapshot the pinger metrics
	c.metrics.Mu.Lock()
	avgPing := c.metrics.AvgPing
	lossRatio := c.metrics.LossRatio
	c.metrics.Mu.Unlock()

	avgMs := float64(avgPing) / float64(time.Millisecond)
	prevRate := c.currentRate
	now := time.Now()

	// Cooldown: don't throttle more than once every 2 seconds
	cooldown := 2 * time.Second
	target := c.maxAcceptableMs

	switch {
	// --- Congested: Absolute threshold exceeded ---
	case avgMs > target:
		if now.Sub(c.lastThrottle) < cooldown {
			return
		}

		// Dynamic proportional decrease multiplier (e.g. target=900, avg=1000 -> reduction=10% -> factor 0.5 -> 5% cut)
		// multiplier = 1.0 - (1.0 - (target / avgMs)) * factor
		rawMultiplier := target / avgMs
		reduction := 1.0 - rawMultiplier
		appliedReduction := reduction * c.throttleFactor
		multiplier := 1.0 - appliedReduction

		newRate := int(float64(c.currentRate) * multiplier)
		newRate = clamp(newRate, c.minRate, c.maxRate)

		if newRate < c.currentRate {
			// Calculate percentage above target limit
			excessPct := ((avgMs - target) / target) * 100
			log.Printf("[THROTTLE] Ping spiked to %.1fms (%.1f%% above %vms limit). Cutting bandwidth from %d to %d kbps (raw_mult: %.3f, adjusted_mult: %.3f).",
				avgMs, excessPct, target, c.currentRate, newRate, rawMultiplier, multiplier)
			c.lastThrottle = now
		} else {
			log.Printf("[THROTTLE] Ping spiked to %.1fms, but already at minimum %d kbps.", avgMs, c.currentRate)
		}
		c.currentRate = newRate

	// --- Clear: Below absolute threshold ---
	case avgMs < target:
		// Calculate how far below the limit we are (1.0 = 0ms, 0.0 = exactly at limit)
		distanceRatio := (target - avgMs) / target
		increase := c.maxAdditiveInc * distanceRatio

		// Ensure we always add at least 1 kbps if we consider it "clear"
		if increase < 1.0 {
			increase = 1.0
		}

		newRate := c.currentRate + int(increase)
		newRate = clamp(newRate, c.minRate, c.maxRate)

		if newRate > c.currentRate {
			log.Printf("[CLEAR] Ping stable at %.1fms. Adding %d kbps -> %d kbps.",
				avgMs, int(increase), newRate)
		} else {
			c.maintainCount++
			if c.maintainCount%10 == 0 {
				log.Printf("[MAINTAIN] Ping at %.1fms. Holding at maximum rate %d kbps.", avgMs, c.currentRate)
			}
		}
		c.currentRate = newRate

	// --- Maintenance: Exactly on threshold ---
	default:
		c.maintainCount++
		if c.maintainCount%10 == 0 {
			log.Printf("[MAINTAIN] Ping at %.1fms. Holding steady at %d kbps. (loss=%.0f%%)",
				avgMs, c.currentRate, lossRatio*100)
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

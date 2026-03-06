package main

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

// PingerMetrics holds the latest metrics produced by the Pinger goroutine.
// Protected by Mu for concurrent read/write between Pinger and Controller.
type PingerMetrics struct {
	Mu        sync.Mutex
	AvgPing   time.Duration // rolling average of last WindowSize pings
	MinPing   time.Duration // decaying minimum over MinPingWindow
	LossRatio float64       // 0.0–1.0 in the current window
}

// Pinger sends ICMP pings or TCP probes and maintains rolling statistics.
type Pinger struct {
	host          string
	port          int // if > 0, use TCPing
	interval      time.Duration
	windowSize    int
	minPingWindow time.Duration
	metrics       *PingerMetrics
	samples       []sample // circular buffer for rolling avg
	rttIdx        int
	rttCount      int
	minSamples    []minSample // samples for decaying min
}

type sample struct {
	rtt time.Duration
	ok  bool
}

type minSample struct {
	rtt time.Duration
	at  time.Time
}

func NewPinger(host string, port int, interval time.Duration, windowSize int, minPingWindow time.Duration, metrics *PingerMetrics) *Pinger {
	return &Pinger{
		host:          host,
		port:          port,
		interval:      interval,
		windowSize:    windowSize,
		minPingWindow: minPingWindow,
		metrics:       metrics,
		samples:       make([]sample, windowSize),
	}
}

// Run starts the pinger loop. It blocks until ctx is cancelled via the stop channel.
func (p *Pinger) Run(stop <-chan struct{}) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			rtt, ok := p.measure()
			p.recordSample(rtt, ok)
		}
	}
}

func (p *Pinger) measure() (time.Duration, bool) {
	if p.port > 0 {
		return p.sendTCPPing()
	}
	return p.sendICMPPing()
}

// sendTCPPing measures latency via a TCP handshake.
func (p *Pinger) sendTCPPing() (time.Duration, bool) {
	address := net.JoinHostPort(p.host, fmt.Sprintf("%d", p.port))
	start := time.Now()
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		// Log rarely to avoid spamming if the port is closed or network is down
		return 0, false
	}
	elapsed := time.Since(start)
	conn.Close()
	return elapsed, true
}

// sendICMPPing sends a single ICMP ping and returns the RTT.
// Returns (0, false) on timeout/error.
func (p *Pinger) sendICMPPing() (time.Duration, bool) {
	pinger, err := probing.NewPinger(p.host)
	if err != nil {
		log.Printf("[PINGER] Error creating pinger: %v", err)
		return 0, false
	}
	pinger.Count = 1
	pinger.Timeout = 2 * time.Second
	pinger.SetPrivileged(true) // requires CAP_NET_RAW or root

	err = pinger.Run()
	if err != nil {
		log.Printf("[PINGER] Ping failed: %v", err)
		return 0, false
	}

	stats := pinger.Statistics()
	if stats.PacketsRecv == 0 {
		return 0, false
	}
	return stats.AvgRtt, true
}

// recordSample updates the rolling window and min-ping tracker.
func (p *Pinger) recordSample(rtt time.Duration, received bool) {
	now := time.Now()

	// --- Update rolling RTT window ---
	p.samples[p.rttIdx] = sample{rtt: rtt, ok: received}
	p.rttIdx = (p.rttIdx + 1) % p.windowSize
	if p.rttCount < p.windowSize {
		p.rttCount++
	}

	// Compute AvgPing and LossRatio over the window
	var sum time.Duration
	var lostCount int
	count := p.rttCount
	for i := 0; i < count; i++ {
		if !p.samples[i].ok {
			lostCount++
		} else {
			sum += p.samples[i].rtt
		}
	}

	var avgPing time.Duration
	var lossRatio float64
	if lostCount == count {
		// 100% loss
		lossRatio = 1.0
		avgPing = 0
	} else {
		avgPing = sum / time.Duration(count-lostCount)
		lossRatio = float64(lostCount) / float64(count)
	}

	// --- Update decaying MinPing ---
	if received {
		p.minSamples = append(p.minSamples, minSample{rtt: rtt, at: now})
	}

	// Evict samples older than minPingWindow
	cutoff := now.Add(-p.minPingWindow)
	firstValid := 0
	for firstValid < len(p.minSamples) && p.minSamples[firstValid].at.Before(cutoff) {
		firstValid++
	}
	if firstValid > 0 {
		p.minSamples = p.minSamples[firstValid:]
	}

	// Find the minimum among remaining samples
	var minPing time.Duration
	if len(p.minSamples) > 0 {
		minPing = p.minSamples[0].rtt
		for _, s := range p.minSamples[1:] {
			if s.rtt < minPing {
				minPing = s.rtt
			}
		}
	}

	// --- Publish metrics ---
	p.metrics.Mu.Lock()
	p.metrics.AvgPing = avgPing
	p.metrics.MinPing = minPing
	p.metrics.LossRatio = lossRatio
	p.metrics.Mu.Unlock()
}

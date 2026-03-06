package main

import (
	"log"
	"net/http"
	"sync"
	"time"
)

// PingerMetrics holds the latest metrics produced by the Pinger goroutine.
// Protected by Mu for concurrent read/write between Pinger and Controller.
type PingerMetrics struct {
	Mu        sync.Mutex
	AvgPing   time.Duration // rolling average of last WindowSize pings
	MinPing   time.Duration // decaying minimum over MinPingWindow
	LossRatio float64       // 0.0–1.0 in the current window
}

// Pinger sends HTTPS probes to rotating URLs and maintains rolling statistics.
type Pinger struct {
	urls          []string
	interval      time.Duration
	windowSize    int
	minPingWindow time.Duration
	metrics       *PingerMetrics
	samples       []sample // circular buffer for rolling avg
	rttIdx        int
	rttCount      int
	urlIdx        int
	minSamples    []minSample // samples for decaying min
	client        *http.Client
}

type sample struct {
	rtt time.Duration
	ok  bool
}

type minSample struct {
	rtt time.Duration
	at  time.Time
}

func NewPinger(urls []string, interval time.Duration, windowSize int, minPingWindow time.Duration, metrics *PingerMetrics) *Pinger {
	return &Pinger{
		urls:          urls,
		interval:      interval,
		windowSize:    windowSize,
		minPingWindow: minPingWindow,
		metrics:       metrics,
		samples:       make([]sample, windowSize),
		client: &http.Client{
			Timeout: 2 * time.Second,
			Transport: &http.Transport{
				Proxy: nil, // Bypass any system proxies
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
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
	url := p.urls[p.urlIdx]
	p.urlIdx = (p.urlIdx + 1) % len(p.urls)

	start := time.Now()
	resp, err := p.client.Get(url)
	if err != nil {
		log.Printf("[PINGER] HTTP Probe to %s failed: %v", url, err)
		return 0, false
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	// Log first success to confirm it's actually reaching out
	if p.rttCount < 5 {
		remote := resp.Request.URL.Host
		log.Printf("[PINGER] Probe to %s (%s) success: status=%d, rtt=%v", url, remote, resp.StatusCode, elapsed)
	}

	return elapsed, true
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

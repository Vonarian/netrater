package main

import (
	"log"
	"net"
	"net/http"
	"net/url"
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
	proxyStr      string
	ipCache       map[string]string // domain -> ip
	cacheMu       sync.RWMutex
}

type sample struct {
	rtt time.Duration
	ok  bool
}

type minSample struct {
	rtt time.Duration
	at  time.Time
}

func NewPinger(urls []string, proxyStr string, interval time.Duration, windowSize int, minPingWindow time.Duration, metrics *PingerMetrics) *Pinger {
	p := &Pinger{
		urls:          urls,
		proxyStr:      proxyStr,
		interval:      interval,
		windowSize:    windowSize,
		minPingWindow: minPingWindow,
		metrics:       metrics,
		samples:       make([]sample, windowSize),
		ipCache:       make(map[string]string),
	}

	transport := &http.Transport{
		DisableKeepAlives: true, // Force new connection to measure latency
	}

	if proxyStr != "" {
		proxyURL, err := url.Parse(proxyStr)
		if err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		} else {
			log.Printf("[PINGER] Invalid proxy URL %s: %v", proxyStr, err)
		}
	}

	p.client = &http.Client{
		Timeout:   2 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return p
}

// MeasureAndRecord performs a single probe and records the result.
func (p *Pinger) MeasureAndRecord() {
	rtt, ok := p.measure()
	p.recordSample(rtt, ok)
}

// ResolveIPs refreshes the IP cache for all target domains.
func (p *Pinger) ResolveIPs() {
	log.Println("[PINGER] Refreshing IP cache for domains...")
	for _, uStr := range p.urls {
		parsed, err := url.Parse(uStr)
		if err != nil {
			continue
		}
		host := parsed.Hostname()
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			log.Printf("[PINGER] Failed to resolve %s: %v", host, err)
			continue
		}
		// Pick the first IPv4 if possible
		var targetIP string
		for _, ip := range ips {
			if ip.To4() != nil {
				targetIP = ip.String()
				break
			}
		}
		if targetIP == "" {
			targetIP = ips[0].String()
		}

		p.cacheMu.Lock()
		p.ipCache[host] = targetIP
		p.cacheMu.Unlock()
		log.Printf("[PINGER] Resolved %s -> %s", host, targetIP)
	}
}

func (p *Pinger) measure() (time.Duration, bool) {
	uStr := p.urls[p.urlIdx]
	p.urlIdx = (p.urlIdx + 1) % len(p.urls)

	parsed, err := url.Parse(uStr)
	if err != nil {
		return 0, false
	}

	host := parsed.Hostname()
	p.cacheMu.RLock()
	ip := p.ipCache[host]
	p.cacheMu.RUnlock()

	targetURL := uStr
	if ip != "" {
		// Replace host with IP in URL for direct connection
		// Note: We MUST keep the original host in the 'Host' header for HTTPS/SNI
		parsed.Host = net.JoinHostPort(ip, parsed.Port())
		targetURL = parsed.String()
	}

	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return 0, false
	}
	// Restore host header for SNI and virtual hosting
	req.Host = host

	start := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		log.Printf("[PINGER] HTTP Probe to %s failed: %v", uStr, err)
		return 0, false
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	// Log first few successes or if we see a change
	if p.rttCount < 5 {
		log.Printf("[PINGER] Probe to %s (%s) success: rtt=%v", uStr, ip, elapsed)
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

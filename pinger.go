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
	LossRatio float64       // 0.0–1.0 in the current window (for tracking, not throttling)
}

// Pinger sends HTTPS probes to rotating URLs and maintains rolling statistics.
type Pinger struct {
	urls           []string
	interval       time.Duration
	windowSize     int
	minPingWindow  time.Duration
	metrics        *PingerMetrics
	samples        []sample // circular buffer for rolling avg
	rttIdx         int
	rttCount       int
	urlIdx         int
	client         *http.Client
	proxyStr       string
	ipCache        map[string]string // domain -> ip
	cacheMu        sync.RWMutex
	timeoutPenalty time.Duration
}

type sample struct {
	rtt time.Duration
	ok  bool
}

func NewPinger(urls []string, proxyStr string, interval time.Duration, windowSize int, timeoutPenaltyMs float64, metrics *PingerMetrics) *Pinger {
	p := &Pinger{
		urls:           urls,
		proxyStr:       proxyStr,
		interval:       interval,
		windowSize:     windowSize,
		timeoutPenalty: time.Duration(timeoutPenaltyMs * float64(time.Millisecond)),
		metrics:        metrics,
		samples:        make([]sample, windowSize),
		ipCache:        make(map[string]string),
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

	req, err := http.NewRequest("HEAD", targetURL, nil)
	if err != nil {
		return 0, false
	}
	// Restore host header for SNI and virtual hosting
	req.Host = host

	start := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		log.Printf("[PINGER][ERROR] HTTP Probe to %s failed: %v", uStr, err)
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

// recordSample updates the rolling window.
func (p *Pinger) recordSample(rtt time.Duration, received bool) {

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
			sum += p.timeoutPenalty // Treat loss as maximum timeout to organically spike the average
		} else {
			sum += p.samples[i].rtt
		}
	}

	var avgPing time.Duration
	var lossRatio float64
	if count > 0 {
		avgPing = sum / time.Duration(count)
		lossRatio = float64(lostCount) / float64(count)
	}

	// --- Publish metrics ---
	p.metrics.Mu.Lock()
	p.metrics.AvgPing = avgPing
	p.metrics.LossRatio = lossRatio
	p.metrics.Mu.Unlock()
}

# NetRater – Autonomous SD-WAN & QoS Appliance

NetRater is a custom-engineered, dynamic traffic shaping and routing appliance built for the HP T630 thin client. It is designed to survive extremely hostile network environments (severe throttling, bufferbloat, and high packet loss) by combining hardware-level queuing with an active AIMD (Additive Increase, Multiplicative Decrease) bandwidth controller.

## 🏗️ Architecture Overview

This system operates on three distinct layers to ensure VIP devices (like mobile phones or work laptops) remain perfectly responsive even when the upstream ISP connection is heavily congested.

1. **The Traffic Cop (`setup-qos.sh`)**
   - Uses `iptables` to mark packets based on their source/destination IP (VIP vs. Bulk).
   - Uses Linux Traffic Control (`tc`) with HTB (Hierarchical Token Bucket) to create a strict hierarchy where VIP traffic guarantees bandwidth and borrows heavily from Bulk.
   - Applies `fq_codel` to all leaf classes to mathematically eliminate bufferbloat and ensure fair queuing within the classes.

2. **The Active Controller (`controller.go` / `main.go`)**
   - A Go daemon that constantly measures real-world latency by sending sequential `HEAD` requests to rotating Google `generate_204` URLs.
   - If average ping exceeds the target (e.g., 900ms), it dynamically shrinks the physical interface ceiling to force packets to queue locally on the T630 rather than dropping at the ISP.
   - If ping is stable, it slowly opens the bandwidth pipe back up to probe for maximum throughput.

3. **The Isolated Routing Core (Xray)**
   - Traffic is transparently proxied via Xray.
   - Xray points to a LAN `192.168.8.114` SOCKS5 proxy (Dante on another PC), which is bound to `ppp0` OpenFortiVPN interface.
   - Features perfect MTU matching (1500) and TCP MSS Clamping (1460) to eliminate fragmentation overhead.

---

## 📂 Directory Structure

```text
/netrater
├── main.go             # Entry point, sequential ping/evaluate loop
├── controller.go       # AIMD logic (proportional cuts, additive increases)
├── pinger.go           # Latency measurement and rolling averages
├── executor.go         # Interfaces with Linux `tc` to apply dynamic ceilings
├── setup-qos.sh        # Builds the iptables and HTB/fq_codel hierarchy
├── teardown-qos.sh     # Safely flushes routing marks and qdiscs
├── netrater.service    # systemd unit to orchestrate startup/teardown
└── go.mod              # Go module definitions
```

---

## ⚙️ Key Tunables & Configuration

### 1. VIP Device Allocation

To add or remove devices from the fast lane, edit the `VIP_IPS` array in `setup-qos.sh`:

```bash
VIP_IPS=("192.168.50.2" "192.168.50.3") # Add static IPs here

```

### 2. Bandwidth Bounds (`main.go`)

The controller dynamically adjusts the network ceiling between these bounds.
_Note: The `executor.go` scales the VIP ceiling dynamically, clamped by a hard `MaxVIPRate` to ensure it never exceeds physical ISP limits._

```go
const (
	MinRate      = 1500  // Floor rate (kbps). NetRater will never throttle below this.
	MaxRate      = 8196  // Max base rate (kbps) when network is perfectly clear.
	MaxVIPRate   = 12288 // Hard limit for the VIP class ceiling (~12 Mbps).
)

```

### 3. Controller Aggressiveness (`main.go`)

```go
const (
	MaxAcceptableRTTMs  = 900.0 // Latency threshold before the axe swings
	ThrottleFactor      = 0.5   // Softens the AIMD cut (0.5 = 50% of the calculated proportional cut)
	MaxAdditiveIncrease = 250.0 // Max kbps added per successful interval
)

```

---

## 🚀 Deployment & Usage

**1. Compile the Binary:**

```bash
go build -o netrater .
sudo mv netrater /usr/local/bin/

```

**2. Install the Scripts:**

```bash
sudo cp setup-qos.sh teardown-qos.sh /usr/local/bin/
sudo chmod +x /usr/local/bin/setup-qos.sh /usr/local/bin/teardown-qos.sh

```

**3. Install the Service:**

```bash
sudo cp netrater.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now netrater

```

**4. Monitor Live Performance:**

```bash
# Watch the NetRater daemon logs:
journalctl -u netrater -f

# Watch real-time tc queuing statistics:
watch -n 1 "tc -s class show dev wlx50ebf65e7306"

```

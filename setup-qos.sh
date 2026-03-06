#!/bin/bash
# ---------------------------------------------------------
# Dynamic VIP QoS Setup for T630
# ---------------------------------------------------------

WIFI_5G="wlx50ebf65e7306"

# Your exact VIP list
VIP_IPS=(
    "192.168.50.2" # S25 Ultra
    "192.168.50.3" # ROG Laptop
    "192.168.50.4" # MacBook Pro
    "192.168.50.5" # Redmi
)

echo "--- [1/3] Flushing Old Rules ---"
# Remove old firewall marks to prevent duplicates
iptables -t mangle -D PREROUTING -j VIP_MARK 2>/dev/null || true
iptables -t mangle -D POSTROUTING -j VIP_MARK 2>/dev/null || true
iptables -t mangle -F VIP_MARK 2>/dev/null || true
iptables -t mangle -X VIP_MARK 2>/dev/null || true

# Remove old Traffic Control qdiscs
tc qdisc del dev $WIFI_5G root 2>/dev/null || true

echo "--- [2/3] Tagging VIP Traffic ---"
# Create a custom chain for clean management
iptables -t mangle -N VIP_MARK
iptables -t mangle -I PREROUTING -j VIP_MARK
iptables -t mangle -I POSTROUTING -j VIP_MARK

# Loop through the hit list and mark their traffic with 0x10 (16)
for IP in "${VIP_IPS[@]}"; do
    iptables -t mangle -A VIP_MARK -s "$IP" -j MARK --set-mark 0x10
    iptables -t mangle -A VIP_MARK -d "$IP" -j MARK --set-mark 0x10
done

echo "--- [3/3] Building HTB QoS Buckets ---"
# 1. Add root HTB qdisc
tc qdisc add dev $WIFI_5G root handle 1: htb default 20

# 2. Create the PARENT class (The Go Daemon will resize this later! Default: 10mbit)
tc class add dev $WIFI_5G parent 1: classid 1:1 htb rate 10mbit

# 3. Create the VIP Class (Prio 1 - gets first dibs on the parent bandwidth)
tc class add dev $WIFI_5G parent 1:1 classid 1:10 htb rate 10mbit prio 1

# 4. Create the BULK Class (Prio 2 - gets whatever is leftover)
tc class add dev $WIFI_5G parent 1:1 classid 1:20 htb rate 10mbit prio 2

# 5. Direct the marked VIP packets (0x10) straight into the VIP Class (1:10)
tc filter add dev $WIFI_5G protocol ip parent 1: prio 1 handle 0x10 fw flowid 1:10

echo "--- VIP Priority Network Active! ---"
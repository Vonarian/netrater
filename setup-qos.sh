#!/bin/bash
# /usr/local/bin/setup-qos.sh

WIFI_5G="wlx50ebf65e7306"
VIP_IPS=("192.168.50.2" "192.168.50.3" "192.168.50.4" "192.168.50.5")

# --- [1] Reset State ---
iptables -t mangle -D PREROUTING -j VIP_MARK 2>/dev/null || true
iptables -t mangle -D POSTROUTING -j VIP_MARK 2>/dev/null || true
iptables -t mangle -F VIP_MARK 2>/dev/null || true
iptables -t mangle -X VIP_MARK 2>/dev/null || true
tc qdisc del dev $WIFI_5G root 2>/dev/null || true

# --- [2] VIP Tagging ---
iptables -t mangle -N VIP_MARK
iptables -t mangle -I PREROUTING -j VIP_MARK
iptables -t mangle -I POSTROUTING -j VIP_MARK

# [NEW] [Bypass] Mark all 192.168.x.x traffic as 0x20 and skip further marking
iptables -t mangle -A VIP_MARK -d 192.168.0.0/16 -j MARK --set-mark 0x20
iptables -t mangle -A VIP_MARK -d 192.168.0.0/16 -j RETURN

for IP in "${VIP_IPS[@]}"; do
    iptables -t mangle -A VIP_MARK -s "$IP" -j MARK --set-mark 0x10
    iptables -t mangle -A VIP_MARK -d "$IP" -j MARK --set-mark 0x10
done

# --- [3] Build HTB Hierarchy ---
# Root defaults to class 30 (Bulk)
tc qdisc add dev $WIFI_5G root handle 1: htb default 30

# LAN Bypass (Class 1:10) - Pure Gigabit, bypasses NetRater throttle
tc class add dev $WIFI_5G parent 1: classid 1:10 htb rate 1000mbit prio 0

# THE THROTTLE PARENT (Class 1:1) - Controlled by NetRater (e.g., 2 * BaseRate)
tc class add dev $WIFI_5G parent 1: classid 1:1 htb rate 8mbit

# VIP SUB-CLASS (Class 1:20) - Nested inside 1:1, Priority 1, 2x Ceil
tc class add dev $WIFI_5G parent 1:1 classid 1:20 htb rate 4mbit ceil 8mbit prio 1

# BULK SUB-CLASS (Class 1:30) - Nested inside 1:1, Priority 2, 1x Ceil
tc class add dev $WIFI_5G parent 1:1 classid 1:30 htb rate 100kbit ceil 4mbit prio 2

# --- [4] Fairness (fq_codel) ---
# Add fq_codel to leaf classes for per-flow fairness and low latency
tc qdisc add dev $WIFI_5G parent 1:10 handle 10: fq_codel
tc qdisc add dev $WIFI_5G parent 1:20 handle 20: fq_codel
tc qdisc add dev $WIFI_5G parent 1:30 handle 30: fq_codel

# --- [4] Apply Filters ---
# LAN traffic (Mark 0x20) to bypass class
tc filter add dev $WIFI_5G protocol ip parent 1: prio 0 handle 0x20 fw flowid 1:10
# VIP traffic (Mark 0x10) to VIP class
tc filter add dev $WIFI_5G protocol ip parent 1: prio 1 handle 0x10 fw flowid 1:20
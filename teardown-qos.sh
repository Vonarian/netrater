#!/bin/bash
# /usr/local/bin/teardown-qos.sh

WIFI_5G="wlx50ebf65e7306"

# --- [1] Reset State ---
echo "[TEARDOWN] Removing VIP_MARK iptables rules..."
iptables -t mangle -D PREROUTING -j VIP_MARK 2>/dev/null || true
iptables -t mangle -D POSTROUTING -j VIP_MARK 2>/dev/null || true
iptables -t mangle -F VIP_MARK 2>/dev/null || true
iptables -t mangle -X VIP_MARK 2>/dev/null || true

echo "[TEARDOWN] Deleting tc qdisc hierarchy on $WIFI_5G..."
tc qdisc del dev $WIFI_5G root 2>/dev/null || true

echo "[TEARDOWN] Done."

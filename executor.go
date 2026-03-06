package main

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// Executor applies bandwidth changes to the Linux tc subsystem.
type Executor struct {
	iface   string
	classID string
}

func NewExecutor(iface, classID string) *Executor {
	return &Executor{
		iface:   iface,
		classID: classID,
	}
}

// Setup initializes the root qdisc on the interface if it doesn't already exist.
func (e *Executor) Setup() error {
	// 1. Delete existing root qdisc to clear old state. Ignore errors as it may not exist.
	exec.Command("tc", "qdisc", "del", "dev", e.iface, "root").Run()

	// Parse default class from e.classID (e.g., "1:1" -> "1").
	parts := strings.Split(e.classID, ":")
	defaultClass := "1"
	if len(parts) == 2 {
		defaultClass = parts[1]
	}

	// 2. Add root qdisc, defaulting traffic to the configured class.
	cmd := exec.Command("tc", "qdisc", "add",
		"dev", e.iface,
		"root", "handle", "1:",
		"htb", "default", defaultClass,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[EXECUTOR] Failed to setup root qdisc: %v | output: %s", err, string(out))
		return fmt.Errorf("tc qdisc add failed: %w", err)
	}

	// 3. Create an unshaped class (1:10) for local network traffic bypass
	bypassClassFunc := func() error {
		cmd = exec.Command("tc", "class", "add",
			"dev", e.iface,
			"parent", "1:",
			"classid", "1:10",
			"htb", "rate", "1000mbit",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("[EXECUTOR] Failed to create local bypass class: %v | output: %s", err, string(out))
			return fmt.Errorf("tc class add failed: %w", err)
		}

		// 4. Route local subnets to the bypass class
		subnets := []string{"192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12"}
		for _, subnet := range subnets {
			cmd := exec.Command("tc", "filter", "add",
				"dev", e.iface,
				"protocol", "ip",
				"parent", "1:",
				"prio", "1",
				"u32", "match", "ip", "dst", subnet,
				"flowid", "1:10",
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				log.Printf("[EXECUTOR] Failed to add filter for %s: %v | output: %s", subnet, err, string(out))
			}
		}
		return nil
	}

	if err := bypassClassFunc(); err != nil {
		// Log but do not fail completely, as the main rate limiter can still function
		log.Printf("[EXECUTOR] Local network bypass setup failed, local traffic will be limited.")
	}

	log.Printf("[EXECUTOR] Initialized HTB root qdisc on %s with local traffic bypass", e.iface)
	return nil
}

// Apply changes the HTB class rate to the given value in kbps.
func (e *Executor) Apply(rateKbps int) error {
	// Add burst/cburst for smoother behavior at low rates
	// burst = rate / hz, but roughly 15k is common for 100Mbit.
	// For AIMD, we want small but existing bursts.
	cmd := exec.Command("tc", "class", "replace",
		"dev", e.iface,
		"parent", "1:",
		"classid", e.classID,
		"htb", "rate", fmt.Sprintf("%dkbit", rateKbps),
		"burst", "15k", "cburst", "15k",
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[EXECUTOR] tc failed: %v | output: %s", err, string(out))
		return fmt.Errorf("tc command failed: %w", err)
	}

	log.Printf("[EXECUTOR] Applied rate %d kbps on %s classid %s", rateKbps, e.iface, e.classID)
	return nil
}

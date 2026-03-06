package main

import (
	"fmt"
	"log"
	"os/exec"
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

// Setup verifies that the network interface exists.
// In passive mode, it does NOT initialize or modify the HTB hierarchy.
func (e *Executor) Setup() error {
	// Verify interface exists
	cmd := exec.Command("ip", "link", "show", "dev", e.iface)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[EXECUTOR] Interface %s not found: %v | output: %s", e.iface, err, string(out))
		return fmt.Errorf("interface check failed: %w", err)
	}

	log.Printf("[EXECUTOR] Passive mode: interface %s verified. Expecting class %s to be managed externally.", e.iface, e.classID)
	return nil
}

// Apply changes the HTB class rate to the given value in kbps.
func (e *Executor) Apply(rateKbps int) error {
	// We use 'replace' to update the existing class.
	// This assumes the class (configured as 1:1 in main.go) was created by setup-qos.sh.
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

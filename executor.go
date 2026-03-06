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

// Apply changes the HTB class rate to the given value in kbps.
func (e *Executor) Apply(rateKbps int) error {
	cmd := exec.Command("tc", "class", "change",
		"dev", e.iface,
		"parent", "1:",
		"classid", e.classID,
		"htb", "rate", fmt.Sprintf("%dkbit", rateKbps),
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[EXECUTOR] tc failed: %v | output: %s", err, string(out))
		return fmt.Errorf("tc command failed: %w", err)
	}

	log.Printf("[EXECUTOR] Applied rate %d kbps on %s classid %s", rateKbps, e.iface, e.classID)
	return nil
}

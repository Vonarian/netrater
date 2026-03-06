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

// Apply changes the tiered HTB class rates to the given base value in kbps.
// It applies baseRate * 2 to the parent and VIP ceil, and baseRate to Bulk ceil.
func (e *Executor) Apply(baseRate int) error {
	parentRate := baseRate * 2

	// 1. Update Parent Class (1:1) - The shared pipe for throttled traffic
	if err := e.replaceClass("1:1", "1:", parentRate, parentRate); err != nil {
		return err
	}

	// 2. Update VIP Class (1:20) - Priority 1, 1x guaranteed, 2x ceiling
	if err := e.replaceClass("1:20", "1:1", baseRate, parentRate); err != nil {
		return err
	}

	// 3. Update Bulk Class (1:30) - Priority 2, 1x ceiling
	if err := e.replaceClass("1:30", "1:1", 100, baseRate); err != nil {
		return err
	}

	log.Printf("[EXECUTOR] Applied tiered rates (Base: %d kbps, VIP Ceil: %d kbps) on %s", baseRate, parentRate, e.iface)
	return nil
}

func (e *Executor) replaceClass(classID, parentID string, rate, ceil int) error {
	cmd := exec.Command("tc", "class", "replace",
		"dev", e.iface,
		"parent", parentID,
		"classid", classID,
		"htb",
		"rate", fmt.Sprintf("%dkbit", rate),
		"ceil", fmt.Sprintf("%dkbit", ceil),
		"burst", "15k", "cburst", "15k",
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[EXECUTOR] tc replace %s failed: %v | output: %s", classID, err, string(out))
		return fmt.Errorf("tc replace %s failed: %w", classID, err)
	}
	return nil
}

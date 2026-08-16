package main

import "testing"

// TestUpgrade_JSONFlag documents that check-mode accepts --json so the
// desktop app never has to parse the human package table.
func TestUpgrade_JSONFlag(t *testing.T) {
	cmd := newUpgradeCmd()
	if cmd.Flags().Lookup("json") == nil {
		t.Error("upgrade missing --json flag")
	}
	if cmd.Flags().Lookup("apply") == nil {
		t.Error("upgrade missing --apply flag")
	}
}

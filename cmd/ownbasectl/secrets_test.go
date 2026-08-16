package main

import (
	"strings"
	"testing"
)

// TestSecretsCmds_JSONFlag documents that every secrets subcommand accepts
// --json so the desktop app never has to parse human prose.
func TestSecretsCmds_JSONFlag(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"list", newSecretsListCmd().Flags().Lookup("json") != nil},
		{"get", newSecretsGetCmd().Flags().Lookup("json") != nil},
		{"set", newSecretsSetCmd().Flags().Lookup("json") != nil},
		{"delete", newSecretsDeleteCmd().Flags().Lookup("json") != nil},
	}
	for _, tc := range cases {
		if !tc.ok {
			t.Errorf("secrets %s missing --json flag", tc.name)
		}
	}
	if newSecretsSetCmd().Flags().Lookup("stdin") == nil {
		t.Error("secrets set missing --stdin flag")
	}
}

// TestSecretsSet_ResticPasswordRefused is the guardrail that keeps restic
// keyring, Base secret, and vault escrow aligned — RESTIC_PASSWORD must go
// through backup rekey, never secrets set. Runs without a live Base.
func TestSecretsSet_ResticPasswordRefused(t *testing.T) {
	err := runSecretsSet("anybase", "backup", []string{"RESTIC_PASSWORD=x"}, false, false)
	if err == nil {
		t.Fatal("expected RESTIC_PASSWORD via secrets set to be refused")
	}
	if !strings.Contains(err.Error(), "backup rekey") {
		t.Errorf("error should point at backup rekey, got: %v", err)
	}
}

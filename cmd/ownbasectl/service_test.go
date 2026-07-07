package main

import (
	"reflect"
	"testing"
)

// ---------------------------------------------------------------------------
// --add-capabilities flag
// ---------------------------------------------------------------------------

// TestServiceAdd_AddCapabilitiesFlag verifies that `service add` registers
// --add-capabilities and parses a comma-separated list into a string slice,
// so a service that binds directly to a privileged port (e.g. traefik/whoami
// on port 80) can restore NET_BIND_SERVICE without hand-editing ownbase.yaml.
func TestServiceAdd_AddCapabilitiesFlag(t *testing.T) {
	cmd := newServiceAddCmd()
	if err := cmd.Flags().Parse([]string{
		"--mirror", "https://github.com/traefik/whoami",
		"--add-capabilities", "NET_BIND_SERVICE,SYS_TIME",
	}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	got, err := cmd.Flags().GetStringSlice("add-capabilities")
	if err != nil {
		t.Fatalf("get add-capabilities: %v", err)
	}
	want := []string{"NET_BIND_SERVICE", "SYS_TIME"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("add-capabilities = %v, want %v", got, want)
	}
}

// TestServiceUpdate_AddCapabilitiesFlag verifies the same flag is available
// on `service update`, and that Changed() correctly distinguishes "flag
// passed" from "flag omitted" — runServiceUpdate relies on this to leave an
// existing service's capabilities untouched when the flag isn't passed.
func TestServiceUpdate_AddCapabilitiesFlag(t *testing.T) {
	cmd := newServiceUpdateCmd()
	if err := cmd.Flags().Parse([]string{"--add-capabilities", "NET_BIND_SERVICE"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if !cmd.Flags().Changed("add-capabilities") {
		t.Error("expected add-capabilities to be marked Changed after being passed")
	}
	got, err := cmd.Flags().GetStringSlice("add-capabilities")
	if err != nil {
		t.Fatalf("get add-capabilities: %v", err)
	}
	if want := []string{"NET_BIND_SERVICE"}; !reflect.DeepEqual(got, want) {
		t.Errorf("add-capabilities = %v, want %v", got, want)
	}

	cmdNoFlag := newServiceUpdateCmd()
	if err := cmdNoFlag.Flags().Parse([]string{"--ref", "v2"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if cmdNoFlag.Flags().Changed("add-capabilities") {
		t.Error("expected add-capabilities to be unchanged when not passed")
	}
}

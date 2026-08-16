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
		"--repo", "https://github.com/traefik/whoami",
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

func TestServiceAdd_OwnbaseAccessFlag(t *testing.T) {
	cmd := newServiceAddCmd()
	if err := cmd.Flags().Parse([]string{
		"--repo", "https://github.com/example/app",
		"--ownbase-access", "status:read,config:write",
	}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	got, err := cmd.Flags().GetStringSlice("ownbase-access")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := []string{"status:read", "config:write"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ownbase-access = %v, want %v", got, want)
	}
}

func TestServiceUpdate_OwnbaseAccessChanged(t *testing.T) {
	cmd := newServiceUpdateCmd()
	if err := cmd.Flags().Parse([]string{"--ownbase-access", "status:read"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cmd.Flags().Changed("ownbase-access") {
		t.Error("expected Changed")
	}
	cmd2 := newServiceUpdateCmd()
	if err := cmd2.Flags().Parse([]string{"--port", "8080"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cmd2.Flags().Changed("ownbase-access") {
		t.Error("expected unchanged when omitted")
	}
}

// TestServiceCmds_DryRunAndJSONFlags documents that add/remove/update all
// accept --dry-run and --json so the desktop app can preview a config-repo
// commit before the operator confirms (same contract as deploy).
func TestServiceCmds_DryRunAndJSONFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		has  func(flag string) bool
	}{
		{"add", func(f string) bool { return newServiceAddCmd().Flags().Lookup(f) != nil }},
		{"remove", func(f string) bool { return newServiceRemoveCmd().Flags().Lookup(f) != nil }},
		{"update", func(f string) bool { return newServiceUpdateCmd().Flags().Lookup(f) != nil }},
	} {
		for _, flag := range []string{"json", "dry-run"} {
			if !tc.has(flag) {
				t.Errorf("service %s missing --%s flag", tc.name, flag)
			}
		}
	}
}

func TestNormalizeOwnbaseAccess(t *testing.T) {
	got, err := normalizeOwnbaseAccess("web", []string{" status:read ", "", "config:write"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"status:read", "config:write"}) {
		t.Errorf("got %v", got)
	}
	// Clear.
	got, err = normalizeOwnbaseAccess("web", []string{"", "  "})
	if err != nil || got != nil {
		t.Fatalf("clear: got %v err %v", got, err)
	}
	// Invalid.
	if _, err := normalizeOwnbaseAccess("web", []string{"not a scope!!!"}); err == nil {
		t.Fatal("expected invalid scope error")
	}
	// Star is valid.
	got, err = normalizeOwnbaseAccess("web", []string{"*"})
	if err != nil || !reflect.DeepEqual(got, []string{"*"}) {
		t.Fatalf("star: %v %v", got, err)
	}
}

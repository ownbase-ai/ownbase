package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestUnknownSubcommandFails ensures a typo'd subcommand is an error rather
// than being silently ignored.
func TestUnknownSubcommandFails(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"statsu"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err == nil {
		t.Fatal("expected error for unknown subcommand, got nil")
	}
}

// TestExtractStatusSection verifies the --json scoping helper returns exactly
// one top-level slice of the status payload, and "{}" when it is absent.
func TestExtractStatusSection(t *testing.T) {
	body := []byte(`{"schema_version":"v3","updates":{"drift":[{"service":"app"}]},"security":{"exposure":{"available":true}}}`)

	got, err := extractStatusSection(body, "updates")
	if err != nil {
		t.Fatalf("extractStatusSection(updates): %v", err)
	}
	if want := `{"drift":[{"service":"app"}]}`; string(got) != want {
		t.Errorf("updates section = %s, want %s", got, want)
	}

	got, err = extractStatusSection(body, "missing")
	if err != nil {
		t.Fatalf("extractStatusSection(missing): %v", err)
	}
	if string(got) != "{}" {
		t.Errorf("missing section = %s, want {}", got)
	}

	if _, err := extractStatusSection([]byte("not json"), "updates"); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

// TestHelpListsAllCommands keeps the top-level help honest: every top-level
// command must be present.
func TestHelpListsAllCommands(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"--help"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("--help: %v", err)
	}
	help := out.String()
	for _, want := range []string{
		"create", "adopt", "list", "delete", "restore", "compile", "plan",
		"apply", "status", "checkup", "updates", "security", "backup",
		"secrets", "forgejo", "upgrade", "version",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("top-level help missing command %q", want)
		}
	}
}

// TestBaseTargetingCommandsRequireName verifies every command that targets a
// Base fails fast with a usage error when the required name argument is
// omitted, rather than silently falling back to some default Base.
func TestBaseTargetingCommandsRequireName(t *testing.T) {
	cases := [][]string{
		{"status"},
		{"checkup"},
		{"updates"},
		{"security"},
		{"upgrade"},
		{"forgejo"},
		{"delete"},
		{"backup", "status"},
		{"secrets", "get", "svc", "key"}, // wrong arg count: missing base name
	}
	for _, args := range cases {
		root := newRootCmd()
		root.SetArgs(args)
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		if err := root.Execute(); err == nil {
			t.Errorf("Execute(%v): expected a usage error for a missing Base name, got nil", args)
		}
	}
}

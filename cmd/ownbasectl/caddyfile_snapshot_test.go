package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadCaddyfileSnapshot locks in the fix for "Local plan always reloads
// Caddy": plan.go/apply.go must distinguish "runtime/Caddyfile exists" from
// "never reconciled yet" instead of always passing an empty, unavailable
// snapshot to reconcile.Diff (which forces a reload every time).
func TestReadCaddyfileSnapshot(t *testing.T) {
	t.Run("no runtime dir at all", func(t *testing.T) {
		dir := t.TempDir()
		content, available := readCaddyfileSnapshot(dir)
		if available {
			t.Errorf("available = true, want false (no runtime/ dir)")
		}
		if content != "" {
			t.Errorf("content = %q, want empty", content)
		}
	})

	t.Run("runtime dir but no Caddyfile", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "runtime"), 0o755); err != nil {
			t.Fatalf("mkdir runtime: %v", err)
		}
		_, available := readCaddyfileSnapshot(dir)
		if available {
			t.Errorf("available = true, want false (no Caddyfile written yet)")
		}
	})

	t.Run("existing Caddyfile snapshot", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "runtime"), 0o755); err != nil {
			t.Fatalf("mkdir runtime: %v", err)
		}
		want := "example { respond \"ok\" }\n"
		if err := os.WriteFile(filepath.Join(dir, "runtime", "Caddyfile"), []byte(want), 0o644); err != nil {
			t.Fatalf("write Caddyfile: %v", err)
		}
		content, available := readCaddyfileSnapshot(dir)
		if !available {
			t.Fatal("available = false, want true")
		}
		if content != want {
			t.Errorf("content = %q, want %q", content, want)
		}
	})
}

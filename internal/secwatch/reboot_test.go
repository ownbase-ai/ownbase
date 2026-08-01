package secwatch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGatherRebootRequired_NoMarker(t *testing.T) {
	// On any machine that is not mid-upgrade, the marker is absent.
	// We cannot force its absence under /var/run from a test, so this
	// only asserts the zero value when the real path is missing — which
	// is always true on CI and on a Mac.
	if _, err := os.Stat(rebootRequiredPath); err == nil {
		t.Skip("host actually needs a reboot; cannot assert the empty case")
	}
	got := GatherRebootRequired()
	if got.Required {
		t.Errorf("GatherRebootRequired().Required = true, want false when marker absent")
	}
	if len(got.Packages) != 0 {
		t.Errorf("Packages = %v, want empty", got.Packages)
	}
}

func TestGatherRebootAt_WithPkgs(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "reboot-required")
	pkgs := filepath.Join(dir, "reboot-required.pkgs")
	if err := os.WriteFile(marker, []byte("*** System restart required ***\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pkgs, []byte("linux-image-6.8.0-71-generic\nlibc6\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := gatherRebootAt(marker, pkgs)
	if !got.Required {
		t.Fatal("Required = false, want true")
	}
	want := []string{"linux-image-6.8.0-71-generic", "libc6"}
	if len(got.Packages) != len(want) {
		t.Fatalf("Packages = %v, want %v", got.Packages, want)
	}
	for i := range want {
		if got.Packages[i] != want[i] {
			t.Errorf("Packages[%d] = %q, want %q", i, got.Packages[i], want[i])
		}
	}
}

func TestGatherRebootAt_MarkerOnly(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "reboot-required")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got := gatherRebootAt(marker, filepath.Join(dir, "missing.pkgs"))
	if !got.Required {
		t.Fatal("Required = false, want true")
	}
	if len(got.Packages) != 0 {
		t.Errorf("Packages = %v, want empty when pkgs file missing", got.Packages)
	}
}

func TestGatherRebootAt_NoMarker(t *testing.T) {
	dir := t.TempDir()
	got := gatherRebootAt(filepath.Join(dir, "absent"), filepath.Join(dir, "absent.pkgs"))
	if got.Required {
		t.Error("Required = true, want false")
	}
}

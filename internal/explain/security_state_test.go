package explain

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSecurityStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "security.json")

	if err := MarkPatched(path); err != nil {
		t.Fatal(err)
	}
	st := LoadSecurityState(path)
	if st.LastPatchAt.IsZero() {
		t.Fatal("LastPatchAt not set")
	}
	if st.RescanOnBoot {
		t.Fatal("RescanOnBoot should be false after MarkPatched alone")
	}

	if err := MarkRescanOnBoot(path); err != nil {
		t.Fatal(err)
	}
	st = LoadSecurityState(path)
	if !st.RescanOnBoot {
		t.Fatal("RescanOnBoot not set")
	}
	if st.LastPatchAt.IsZero() {
		t.Fatal("MarkRescanOnBoot must preserve LastPatchAt")
	}

	if err := ClearRescanOnBoot(path); err != nil {
		t.Fatal(err)
	}
	st = LoadSecurityState(path)
	if st.RescanOnBoot {
		t.Fatal("RescanOnBoot should clear")
	}
	// LastPatchAt survives clear.
	if time.Since(st.LastPatchAt) > time.Minute {
		t.Fatalf("LastPatchAt drifted: %v", st.LastPatchAt)
	}
}

func TestLoadSecurityStateMissing(t *testing.T) {
	st := LoadSecurityState(filepath.Join(t.TempDir(), "nope.json"))
	if !st.LastPatchAt.IsZero() || st.RescanOnBoot {
		t.Fatalf("missing file should yield zero state: %+v", st)
	}
}

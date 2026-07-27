package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/vault"
)

// stubMultipass puts a fake "multipass" ahead of the real one on PATH, always
// reporting no VMs. collectBases has no injection seam for vmhost.New(), so
// this is what keeps the test from seeing whatever Multipass VMs happen to
// exist on the machine running it.
func stubMultipass(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"'list --format json') echo '{\"list\":[]}' ;;\n" +
		"*) ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(binDir, "multipass"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
}

// `list --json` with no Bases must print `[]`, not `null` — the same contract
// `sessions list --json` and `checkup --json` already hold, so a caller never
// needs a nil check before ranging over the result. The desktop app's own
// `bases ?? []` fallback is exactly the kind of workaround this is meant to
// make unnecessary.
func TestRunBaseList_JSONEmptyIsArrayNotNull(t *testing.T) {
	startTestAgent(t)
	stubMultipass(t)

	out := captureStdout(t, func() {
		if err := runBaseList(true); err != nil {
			t.Fatalf("runBaseList: %v", err)
		}
	})

	trimmed := strings.TrimSpace(out)
	if trimmed != "[]" {
		t.Errorf("output = %q, want the literal array %q", trimmed, "[]")
	}

	var bases []listedBase
	if err := json.Unmarshal([]byte(out), &bases); err != nil {
		t.Fatalf("output did not parse as JSON: %v", err)
	}
	if bases == nil {
		t.Error("unmarshaled into a nil slice — encoder emitted null")
	}
	if len(bases) != 0 {
		t.Errorf("got %d bases, want 0", len(bases))
	}
}

// With at least one Base, the array still round-trips as expected — this is
// mostly a guard against a fix for the empty case accidentally breaking the
// non-empty one.
func TestRunBaseList_JSONWithBases(t *testing.T) {
	startTestAgent(t)
	stubMultipass(t)
	privPEM, pubLine, _ := newTestOwnerKey(t)
	putTestProfile(t, "mybase", vault.Profile{PrivateKey: privPEM, PublicKey: pubLine})

	out := captureStdout(t, func() {
		if err := runBaseList(true); err != nil {
			t.Fatalf("runBaseList: %v", err)
		}
	})

	var bases []listedBase
	if err := json.Unmarshal([]byte(out), &bases); err != nil {
		t.Fatalf("output did not parse as JSON: %v", err)
	}
	if len(bases) != 1 || bases[0].Name != "mybase" {
		t.Errorf("bases = %+v, want one entry named mybase", bases)
	}
}

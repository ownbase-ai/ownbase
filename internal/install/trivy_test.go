package install

// trivy_test.go: Tier-1 tests for the trivy install step and its dependency
// on podman.socket (see the doc comment on podmanSocketUnit for why Trivy
// needs it). Real systemctl/trivy binaries are stubbed via PATH so these run
// on any host, including a developer's Mac.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// stateEnvVar names the env var the fake systemctl script uses to find its
// on/off marker file, since a PATH-stubbed script has no other way to reach
// into the test's temp dir.
const stateEnvVar = "OWNBASE_TEST_PODMAN_SOCKET_STATE"

// stubSystemctl puts a fake "systemctl" ahead of the real one on PATH. It
// tracks podman.socket's active state in a marker file: absent means
// inactive, present means active. "enable --now podman.socket" creates the
// marker, so a test can observe ensureTrivy actually flipping the socket on.
func stubSystemctl(t *testing.T, startActive bool) (binDir, stateFile string) {
	t.Helper()
	binDir = t.TempDir()
	stateFile = filepath.Join(t.TempDir(), "podman-socket-active")
	if startActive {
		if err := os.WriteFile(stateFile, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	script := `#!/bin/sh
case "$1 $2" in
  "is-active podman.socket")
    if [ -f "$` + stateEnvVar + `" ]; then echo active; exit 0; else echo inactive; exit 3; fi ;;
  "enable --now")
    touch "$` + stateEnvVar + `"; exit 0 ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv(stateEnvVar, stateFile)
	return binDir, stateFile
}

// stubTrivy puts a fake "trivy" on the same PATH dir systemctl was stubbed
// into, so both fakes are found together. It only needs to answer --version.
func stubTrivy(t *testing.T, binDir string) {
	t.Helper()
	script := "#!/bin/sh\necho 'Version: 0.99.9'\n"
	if err := os.WriteFile(filepath.Join(binDir, "trivy"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestPodmanSocketActive_ReflectsSystemctl(t *testing.T) {
	ctx := context.Background()

	t.Run("inactive", func(t *testing.T) {
		stubSystemctl(t, false)
		if podmanSocketActive(ctx) {
			t.Error("podmanSocketActive() = true, want false")
		}
	})

	t.Run("active", func(t *testing.T) {
		stubSystemctl(t, true)
		if !podmanSocketActive(ctx) {
			t.Error("podmanSocketActive() = false, want true")
		}
	})
}

// A trivy binary alone must not be enough: without the socket it depends on,
// every image scan fails the same way ct-mx-1's did, so checkTrivyState must
// not report Done until both are true.
func TestCheckTrivyState_RequiresPodmanSocketToo(t *testing.T) {
	ctx := context.Background()
	binDir, _ := stubSystemctl(t, false)
	stubTrivy(t, binDir)

	s := checkTrivyState(ctx)
	if s.Done {
		t.Errorf("checkTrivyState() with podman.socket inactive: Done = true, want false (detail: %q)", s.Detail)
	}

	// stubSystemctl points PATH at a fresh binDir, so trivy must be
	// re-stubbed into that same directory.
	binDir, _ = stubSystemctl(t, true)
	stubTrivy(t, binDir)

	s = checkTrivyState(ctx)
	if !s.Done {
		t.Errorf("checkTrivyState() with both present: Done = false, want true (detail: %q)", s.Detail)
	}
}

// ensureTrivy must enable the socket itself when trivy is already installed
// but the socket is not — the state a Base ends up in when podman's package
// shipped the unit disabled, which is exactly what happened on ct-mx-1.
func TestEnsureTrivy_EnablesPodmanSocketWhenTrivyAlreadyInstalled(t *testing.T) {
	ctx := context.Background()
	binDir, stateFile := stubSystemctl(t, false)
	stubTrivy(t, binDir)

	if _, err := os.Stat(stateFile); err == nil {
		t.Fatal("podman.socket marker already exists before ensureTrivy ran")
	}

	s := ensureTrivy(ctx, PassZeroConfig{})
	if s.Err != nil {
		t.Fatalf("ensureTrivy: %v", s.Err)
	}
	if !s.Done {
		t.Errorf("ensureTrivy(): Done = false, want true (detail: %q)", s.Detail)
	}
	if _, err := os.Stat(stateFile); err != nil {
		t.Error("ensureTrivy did not enable podman.socket — marker file was never created")
	}
}

func TestEnsureTrivy_DryRunDoesNotEnableSocket(t *testing.T) {
	ctx := context.Background()
	binDir, stateFile := stubSystemctl(t, false)
	stubTrivy(t, binDir)

	s := ensureTrivy(ctx, PassZeroConfig{DryRun: true})
	if s.Done {
		t.Error("ensureTrivy(DryRun): Done = true, want false")
	}
	if _, err := os.Stat(stateFile); err == nil {
		t.Error("ensureTrivy(DryRun) enabled podman.socket — dry-run must not change the host")
	}
}

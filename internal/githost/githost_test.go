package githost_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/githost"
)

// ---------------------------------------------------------------------------
// TrustAllRepos
// ---------------------------------------------------------------------------

func TestTrustAllRepos_SetsSafeDirectoryWildcard(t *testing.T) {
	// TrustAllRepos always targets the real system git config
	// (`git config --system`), so redirect it to a temp file via
	// GIT_CONFIG_SYSTEM (respected by git >= 2.32) instead of touching the
	// test machine's actual /etc/gitconfig.
	cfgPath := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_SYSTEM", cfgPath)

	if err := githost.TrustAllRepos(); err != nil {
		t.Fatalf("TrustAllRepos: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read redirected system gitconfig: %v", err)
	}
	if !strings.Contains(string(data), "directory = *") {
		t.Errorf("expected safe.directory = * in system config, got:\n%s", data)
	}

	// Idempotent: a second call replaces rather than duplicates the value.
	if err := githost.TrustAllRepos(); err != nil {
		t.Fatalf("TrustAllRepos (second call): %v", err)
	}
	data, err = os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read redirected system gitconfig (second read): %v", err)
	}
	if n := strings.Count(string(data), "directory = *"); n != 1 {
		t.Errorf("expected exactly one safe.directory entry after two calls, got %d in:\n%s", n, data)
	}
}

// ---------------------------------------------------------------------------
// MachineID
// ---------------------------------------------------------------------------

func TestMachineID_NonEmpty(t *testing.T) {
	id, err := githost.MachineID()
	if err != nil {
		t.Fatalf("MachineID: %v", err)
	}
	if strings.TrimSpace(id) == "" {
		t.Error("MachineID returned empty string")
	}
}

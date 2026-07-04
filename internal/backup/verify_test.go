package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckPathPresence_PathExistsInRestore(t *testing.T) {
	restoreDir := t.TempDir()
	originalPath := filepath.Join(t.TempDir(), "secrets")

	// restic restores to <restoreDir>/<original path minus leading slash>.
	restored := filepath.Join(restoreDir, strings.TrimPrefix(originalPath, "/"))
	if err := os.MkdirAll(restored, 0o755); err != nil {
		t.Fatal(err)
	}

	result := checkPathPresence(restoreDir, originalPath)
	if !result.Passed {
		t.Errorf("expected pass when path exists in restore, got: %+v", result)
	}
}

func TestCheckPathPresence_MissingFromRestoreButNeverExisted(t *testing.T) {
	restoreDir := t.TempDir()
	// A path that does not exist on this host either — e.g. /opt/ownbase/data
	// on a fresh Base with no services deployed yet. restic silently skips a
	// nonexistent source path rather than failing the snapshot, so this must
	// pass vacuously, not fail the whole verified-restore drill.
	nonexistentOriginal := filepath.Join(t.TempDir(), "does-not-exist", "data")

	result := checkPathPresence(restoreDir, nonexistentOriginal)
	if !result.Passed {
		t.Errorf("expected vacuous pass for a path that never existed, got: %+v", result)
	}
}

func TestCheckPathPresence_MissingFromRestoreButExistsLive(t *testing.T) {
	restoreDir := t.TempDir()
	// A path that DOES exist on the live host but is missing from the
	// restore — a genuine integrity failure (e.g. restic corruption, or the
	// path was added after the last snapshot).
	liveOnly := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(liveOnly, 0o755); err != nil {
		t.Fatal(err)
	}

	result := checkPathPresence(restoreDir, liveOnly)
	if result.Passed {
		t.Errorf("expected failure when path exists live but missing from restore, got: %+v", result)
	}
}

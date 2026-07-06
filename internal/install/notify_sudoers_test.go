package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireVisudo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("visudo"); err != nil {
		t.Skip("visudo not available on this machine")
	}
}

func TestEnsureNotifySudoers_NoopForEmptyOrRootUser(t *testing.T) {
	if err := EnsureNotifySudoers(""); err != nil {
		t.Fatalf("EnsureNotifySudoers(\"\") should be a no-op, got %v", err)
	}
	if err := EnsureNotifySudoers("root"); err != nil {
		t.Fatalf("EnsureNotifySudoers(\"root\") should be a no-op, got %v", err)
	}
	// Neither call should have touched the real system sudoers.d — this
	// test runs unprivileged/on non-Linux machines and must never attempt
	// to write there.
	if _, err := os.Stat(NotifySudoersPath); err == nil {
		t.Fatalf("no-op call must not create %s", NotifySudoersPath)
	}
}

func TestWriteSudoersFile_WritesValidGrant(t *testing.T) {
	requireVisudo(t)

	dest := filepath.Join(t.TempDir(), "ownbase-notify")
	if err := writeSudoersFile(dest, "ubuntu"); err != nil {
		t.Fatalf("writeSudoersFile: %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read %s: %v", dest, err)
	}
	got := string(data)
	if !strings.Contains(got, "ubuntu ALL=(root) NOPASSWD: /usr/bin/pkill -USR1 -F /opt/ownbase/daemon.pid") {
		t.Errorf("unexpected sudoers content:\n%s", got)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat %s: %v", dest, err)
	}
	if perm := info.Mode().Perm(); perm != 0o440 {
		t.Errorf("expected mode 0440, got %o", perm)
	}
}

func TestWriteSudoersFile_Idempotent(t *testing.T) {
	requireVisudo(t)

	dest := filepath.Join(t.TempDir(), "ownbase-notify")
	if err := writeSudoersFile(dest, "ubuntu"); err != nil {
		t.Fatalf("writeSudoersFile (first): %v", err)
	}
	if err := writeSudoersFile(dest, "ubuntu"); err != nil {
		t.Fatalf("writeSudoersFile (second): %v", err)
	}
}

package agentd

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/ownbase/ownbase/internal/vault"
)

// acquireServeLock takes an exclusive flock on ~/.ownbase/agent.lock for the
// critical section in Serve (probe → unlink stale sockets → bind). Without it,
// two concurrent EnsureRunning callers can both see a dead socket, and the
// loser unlinks the winner's freshly bound path — clients then talk to an
// empty agent while the original process still holds the unlocked vault on an
// orphaned inode.
//
// The returned release function unlocks and closes the file. Callers must
// invoke it, including on every error path out of the critical section.
func acquireServeLock() (release func(), err error) {
	path, err := vault.StatePath(ServeLockName)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	var done bool
	return func() {
		if done {
			return
		}
		done = true
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}

package vault

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// FileStore persists the vault as a single local file.
//
// Writes:
//  1. Take an exclusive flock on <path>.lock so concurrent Puts (two
//     CreateStore races, or Create vs KeePassXC) cannot interleave.
//  2. Write ciphertext to a unique temp in the same directory.
//  3. fsync the temp.
//  4. Re-check the CAS condition under the lock.
//  5. os.Rename the temp onto the final path.
//
// The lock is what makes create-if-absent safe: Rename replaces on Unix, so
// without serialization a late creator would clobber the winner. A fixed
// sibling name like path+".new" is deliberately avoided — publishing via
// hard-link while that name still pointed at the live inode let a concurrent
// OpenFile(..., O_TRUNC) wipe the winner's vault.
//
// Version is "mtime_ns/size". Get stats the open file descriptor (not the
// path) so the Version always describes the bytes just read.
type FileStore struct {
	path string
}

// NewFileStore returns a Store backed by the file at path. The parent directory
// is created on the first successful Put.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

// Location returns the filesystem path.
func (s *FileStore) Location() string { return s.path }

// Get reads the file. The Version is taken from the open fd so it cannot
// disagree with the returned bytes under a concurrent writer.
func (s *FileStore) Get(_ context.Context) ([]byte, Version, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, VersionNone, fmt.Errorf("%w: %s", ErrNotExist, s.path)
		}
		return nil, VersionNone, fmt.Errorf("open vault %s: %w", s.path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, VersionNone, fmt.Errorf("read vault %s: %w", s.path, err)
	}
	st, err := f.Stat()
	if err != nil {
		return nil, VersionNone, fmt.Errorf("stat vault %s: %w", s.path, err)
	}
	return data, versionFromInfo(st), nil
}

// Put writes data under the CAS condition described by ifVersion.
func (s *FileStore) Put(_ context.Context, data []byte, ifVersion Version) (Version, error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return VersionNone, fmt.Errorf("create %s: %w", filepath.Dir(s.path), err)
	}

	unlock, err := s.acquireLock()
	if err != nil {
		return VersionNone, err
	}
	defer unlock()

	if err := s.checkCAS(ifVersion); err != nil {
		return VersionNone, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), "."+filepath.Base(s.path)+".tmp.*")
	if err != nil {
		return VersionNone, fmt.Errorf("create temp vault: %w", err)
	}
	tmpPath := tmp.Name()
	// Unique temp: safe to remove on every path. Rename consumes the name on
	// success, so Remove after a successful Rename is a no-op (ENOENT).
	defer func() { _ = os.Remove(tmpPath) }()

	// CreateTemp uses 0600; re-chmod so a looser umask cannot widen it if the
	// runtime ever changes, and so the mode matches what we document.
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return VersionNone, fmt.Errorf("chmod temp vault: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return VersionNone, fmt.Errorf("write vault %s: %w", tmpPath, err)
	}
	// fsync before rename: without it a crash can leave a zero-length or
	// stale file at the final path on filesystems that reorder metadata.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return VersionNone, fmt.Errorf("sync vault %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return VersionNone, fmt.Errorf("write vault %s: %w", tmpPath, err)
	}

	// Re-check under the lock immediately before rename. Another writer is
	// excluded by the flock, but we still verify the stamp so a Put that
	// raced its own Get (before the lock) cannot apply a stale update.
	if err := s.checkCAS(ifVersion); err != nil {
		return VersionNone, err
	}

	if err := os.Rename(tmpPath, s.path); err != nil {
		return VersionNone, fmt.Errorf("replace vault %s: %w", s.path, err)
	}

	ver, err := fileVersion(s.path)
	if err != nil {
		return VersionNone, err
	}
	return ver, nil
}

// checkCAS evaluates the create/update condition against the live path.
// Caller must hold the file lock.
func (s *FileStore) checkCAS(ifVersion Version) error {
	current, statErr := fileVersion(s.path)
	exists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	switch {
	case ifVersion == VersionNone:
		if exists {
			return fmt.Errorf("%w: %s already exists", ErrConflict, s.path)
		}
	default:
		if !exists {
			return fmt.Errorf("%w: %s no longer exists", ErrConflict, s.path)
		}
		if current != ifVersion {
			return fmt.Errorf("%w: %s", ErrConflict, s.path)
		}
	}
	return nil
}

// acquireLock takes an exclusive flock on <path>.lock for the duration of a
// Put. Mirrors internal/agentd's serve lock: the lock file is the mutex; its
// contents are irrelevant.
func (s *FileStore) acquireLock() (unlock func(), err error) {
	lockPath := s.path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open vault lock %s: %w", lockPath, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock vault %s: %w", lockPath, err)
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

// fileVersion returns the mtime_ns/size stamp for path.
func fileVersion(path string) (Version, error) {
	st, err := os.Stat(path)
	if err != nil {
		return VersionNone, err
	}
	return versionFromInfo(st), nil
}

func versionFromInfo(st os.FileInfo) Version {
	return Version(fmt.Sprintf("%d/%d", st.ModTime().UnixNano(), st.Size()))
}

// ensure FileStore satisfies Store at compile time.
var _ Store = (*FileStore)(nil)

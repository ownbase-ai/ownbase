package vault

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// FileStore persists the vault as a single local file. Writes go to a sibling
// temp file, are fsynced, and renamed into place so an interrupted save cannot
// leave a half-written vault where the only copy of an owner key used to be.
//
// Version is "mtime_ns/size", matching the previous in-process stamp format so
// behavior is unchanged for local vaults.
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

// Get reads the file.
func (s *FileStore) Get(_ context.Context) ([]byte, Version, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, VersionNone, fmt.Errorf("%w: %s", ErrNotExist, s.path)
		}
		return nil, VersionNone, fmt.Errorf("read vault %s: %w", s.path, err)
	}
	ver, err := fileVersion(s.path)
	if err != nil {
		// We just read it; a disappearing file mid-call is treated as gone.
		if os.IsNotExist(err) {
			return nil, VersionNone, fmt.Errorf("%w: %s", ErrNotExist, s.path)
		}
		return nil, VersionNone, err
	}
	return data, ver, nil
}

// Put writes data under the CAS condition described by ifVersion.
func (s *FileStore) Put(_ context.Context, data []byte, ifVersion Version) (Version, error) {
	current, statErr := fileVersion(s.path)
	exists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return VersionNone, statErr
	}

	switch {
	case ifVersion == VersionNone:
		if exists {
			return VersionNone, fmt.Errorf("%w: %s already exists", ErrConflict, s.path)
		}
	default:
		if !exists {
			return VersionNone, fmt.Errorf("%w: %s no longer exists", ErrConflict, s.path)
		}
		if current != ifVersion {
			return VersionNone, fmt.Errorf("%w: %s", ErrConflict, s.path)
		}
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return VersionNone, fmt.Errorf("create %s: %w", filepath.Dir(s.path), err)
	}

	tmp := s.path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return VersionNone, fmt.Errorf("write vault %s: %w", tmp, err)
	}
	// Ensure the temp is gone on every error path below.
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return VersionNone, fmt.Errorf("write vault %s: %w", tmp, err)
	}
	// fsync before rename: without it a crash can leave a zero-length or
	// stale file at the final path on filesystems that reorder metadata.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return VersionNone, fmt.Errorf("sync vault %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return VersionNone, fmt.Errorf("write vault %s: %w", tmp, err)
	}

	// Re-check the CAS condition immediately before rename. Between the
	// earlier stat and now another writer (KeePassXC, a second ownbasectl)
	// may have landed; renaming over them would clobber silently.
	if ifVersion == VersionNone {
		if _, err := os.Stat(s.path); err == nil {
			return VersionNone, fmt.Errorf("%w: %s already exists", ErrConflict, s.path)
		} else if !os.IsNotExist(err) {
			return VersionNone, fmt.Errorf("stat vault %s: %w", s.path, err)
		}
	} else {
		now, err := fileVersion(s.path)
		if err != nil {
			if os.IsNotExist(err) {
				return VersionNone, fmt.Errorf("%w: %s no longer exists", ErrConflict, s.path)
			}
			return VersionNone, err
		}
		if now != ifVersion {
			return VersionNone, fmt.Errorf("%w: %s", ErrConflict, s.path)
		}
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return VersionNone, fmt.Errorf("replace vault %s: %w", s.path, err)
	}
	removeTemp = false

	ver, err := fileVersion(s.path)
	if err != nil {
		return VersionNone, err
	}
	return ver, nil
}

// fileVersion returns the mtime_ns/size stamp for path.
func fileVersion(path string) (Version, error) {
	st, err := os.Stat(path)
	if err != nil {
		return VersionNone, err
	}
	return Version(fmt.Sprintf("%d/%d", st.ModTime().UnixNano(), st.Size())), nil
}

// ensure FileStore satisfies Store at compile time.
var _ Store = (*FileStore)(nil)

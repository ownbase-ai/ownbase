package vault

import (
	"context"
	"fmt"
	"strconv"
	"sync"
)

// MemStore is an in-memory Store for tests. It implements real
// compare-and-swap so CAS conflict and retry paths can be exercised without
// a filesystem or network.
type MemStore struct {
	mu      sync.Mutex
	data    []byte
	version uint64
	exists  bool
	name    string
}

// NewMemStore returns an empty in-memory Store. name is the Location() value.
func NewMemStore(name string) *MemStore {
	if name == "" {
		name = "mem"
	}
	return &MemStore{name: name}
}

// Location returns the name given at construction.
func (s *MemStore) Location() string { return s.name }

// Get returns a copy of the current bytes.
func (s *MemStore) Get(_ context.Context) ([]byte, Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.exists {
		return nil, VersionNone, fmt.Errorf("%w: %s", ErrNotExist, s.name)
	}
	out := make([]byte, len(s.data))
	copy(out, s.data)
	return out, memVersion(s.version), nil
}

// Put applies the CAS write.
func (s *MemStore) Put(_ context.Context, data []byte, ifVersion Version) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case ifVersion == VersionNone:
		if s.exists {
			return VersionNone, fmt.Errorf("%w: %s already exists", ErrConflict, s.name)
		}
	default:
		if !s.exists {
			return VersionNone, fmt.Errorf("%w: %s no longer exists", ErrConflict, s.name)
		}
		if memVersion(s.version) != ifVersion {
			return VersionNone, fmt.Errorf("%w: %s", ErrConflict, s.name)
		}
	}

	s.data = make([]byte, len(data))
	copy(s.data, data)
	s.version++
	s.exists = true
	return memVersion(s.version), nil
}

// ForceSet overwrites the store without CAS. Tests use it to simulate a
// foreign writer (KeePassXC, another machine) between a Get and a Put.
func (s *MemStore) ForceSet(data []byte) Version {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = make([]byte, len(data))
	copy(s.data, data)
	s.version++
	s.exists = true
	return memVersion(s.version)
}

func memVersion(n uint64) Version {
	return Version(strconv.FormatUint(n, 10))
}

var _ Store = (*MemStore)(nil)

package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// CachingStore wraps an inner Store with a local ciphertext cache. Reads fall
// back to the cache when the inner Get fails for a reason other than
// ErrNotExist. Writes always go to the inner store; on success the cache is
// updated. Writes never use the cache as a substitute for the authoritative
// store.
//
// The cache holds encrypted KDBX bytes — still requires the master password
// to open — so a stolen cache file is no worse than a stolen vault file.
type CachingStore struct {
	inner Store
	dir   string
	id    string // stable id for this location (hash of Location())

	mu       sync.Mutex
	lastWarn string // last fallback warning, for tests
}

// NewCachingStore returns a CachingStore writing under dir (typically
// ~/.ownbase/cache). dir is created on first write.
func NewCachingStore(inner Store, dir string) *CachingStore {
	sum := sha256.Sum256([]byte(inner.Location()))
	return &CachingStore{
		inner: inner,
		dir:   dir,
		id:    hex.EncodeToString(sum[:8]),
	}
}

// Location delegates to the inner store.
func (c *CachingStore) Location() string { return c.inner.Location() }

// LastWarn returns the most recent cache-fallback warning (tests).
func (c *CachingStore) LastWarn() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastWarn
}

// Get tries the inner store first. On non-NotExist failure, serves the cache
// if present.
func (c *CachingStore) Get(ctx context.Context) ([]byte, Version, error) {
	data, ver, err := c.inner.Get(ctx)
	if err == nil {
		_ = c.writeCache(data, ver)
		return data, ver, nil
	}
	// NotExist is authoritative — do not mask with a stale cache entry that
	// would make Create think a vault exists.
	if errors.Is(err, ErrNotExist) {
		return nil, VersionNone, err
	}
	cached, cver, cerr := c.readCache()
	if cerr != nil {
		return nil, VersionNone, fmt.Errorf("%w (cache miss: %v)", err, cerr)
	}
	msg := fmt.Sprintf("using cached vault for %s — live store unreachable: %v", c.Location(), err)
	c.mu.Lock()
	c.lastWarn = msg
	c.mu.Unlock()
	fmt.Fprintf(os.Stderr, "ownbasectl: warning: %s\n", msg)
	return cached, cver, nil
}

// Put always writes through to the inner store, then refreshes the cache.
func (c *CachingStore) Put(ctx context.Context, data []byte, ifVersion Version) (Version, error) {
	ver, err := c.inner.Put(ctx, data, ifVersion)
	if err != nil {
		return VersionNone, err
	}
	_ = c.writeCache(data, ver)
	return ver, nil
}

func (c *CachingStore) cachePaths() (dataPath, verPath string) {
	base := filepath.Join(c.dir, c.id)
	return base + ".kdbx", base + ".ver"
}

func (c *CachingStore) writeCache(data []byte, ver Version) error {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	dataPath, verPath := c.cachePaths()
	tmp := dataPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, dataPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.WriteFile(verPath, []byte(ver), 0o600)
}

func (c *CachingStore) readCache() ([]byte, Version, error) {
	dataPath, verPath := c.cachePaths()
	data, err := os.ReadFile(dataPath)
	if err != nil {
		return nil, VersionNone, err
	}
	verBytes, err := os.ReadFile(verPath)
	if err != nil {
		return nil, VersionNone, err
	}
	return data, Version(verBytes), nil
}

var _ Store = (*CachingStore)(nil)

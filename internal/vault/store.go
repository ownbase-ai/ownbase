package vault

import (
	"context"
	"errors"
)

// Version is an opaque token identifying a particular generation of the vault
// bytes in a Store. FileStore uses "mtime_ns/size"; an object-store backend
// will use an ETag. Callers must not parse it.
type Version string

// VersionNone is the ifVersion value for Put that means "create only if the
// object does not yet exist". It is also the zero value of Vault.version
// before the first successful read or write.
const VersionNone Version = ""

// ErrNotExist reports that the Store has no vault object yet.
var ErrNotExist = errors.New("vault does not exist in store")

// ErrConflict reports that a conditional Put lost a compare-and-swap: the
// object changed since the Version the caller supplied. Callers should
// re-Get, re-apply their change, and retry.
var ErrConflict = errors.New("vault store conflict: object changed since last read")

// Store is the persistence seam for a vault's ciphertext. The KDBX encode /
// decode path is pure bytes; everything that touches a filesystem, bucket, or
// other medium implements this interface.
//
// Get/Put are the whole surface. There is no separate Exists or Stat: Get
// returns ErrNotExist when absent, and Put's ifVersion carries the
// create-vs-update distinction (see VersionNone).
//
// Concurrency: a Store implementation must make Put atomic with respect to
// readers (no half-written objects) and honor ifVersion as a compare-and-swap.
// Cross-process mutual exclusion beyond CAS is not required — Vault.save
// retries on ErrConflict.
type Store interface {
	// Get returns the current vault ciphertext and its Version.
	// Returns ErrNotExist when no object is present.
	Get(ctx context.Context) (data []byte, version Version, err error)

	// Put writes data. ifVersion selects the write mode:
	//   - VersionNone: succeed only when no object exists (create).
	//   - any other Version: succeed only when the current object still has
	//     that Version (compare-and-swap update).
	// Returns ErrConflict when the condition fails. On success the new
	// Version is returned.
	Put(ctx context.Context, data []byte, ifVersion Version) (Version, error)

	// Location is a human-readable identifier for display and error messages
	// (a filesystem path today; a URL later). It must not contain secrets.
	Location() string
}

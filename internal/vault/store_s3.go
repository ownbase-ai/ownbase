package vault

import (
	"context"
	"errors"
	"fmt"

	"github.com/ownbase/ownbase/internal/objstore"
)

// S3Store persists the vault as a single object in an S3-compatible bucket.
// Version is the object ETag. Create uses If-None-Match: *; update uses
// If-Match: <etag>.
type S3Store struct {
	client *objstore.Client
	key    string
	label  string // secret-free location for display
}

// NewS3Store wraps client for the object at key.
func NewS3Store(client *objstore.Client, key string) *S3Store {
	return &S3Store{
		client: client,
		key:    key,
		label:  "s3://" + key,
	}
}

// Location returns a secret-free identifier.
func (s *S3Store) Location() string {
	if s.label != "" {
		return s.label
	}
	return "s3://" + s.key
}

// SetLocation overrides the display string (used by Locator.OpenStore).
func (s *S3Store) SetLocation(label string) { s.label = label }

// Get fetches the object.
func (s *S3Store) Get(ctx context.Context) ([]byte, Version, error) {
	data, etag, err := s.client.Get(ctx, s.key)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			return nil, VersionNone, fmt.Errorf("%w: %s", ErrNotExist, s.Location())
		}
		return nil, VersionNone, err
	}
	return data, Version(etag), nil
}

// Put writes the object under the CAS condition.
func (s *S3Store) Put(ctx context.Context, data []byte, ifVersion Version) (Version, error) {
	var opts objstore.PutOptions
	if ifVersion == VersionNone {
		opts.IfNoneMatch = "*"
	} else {
		opts.IfMatch = string(ifVersion)
	}
	etag, err := s.client.Put(ctx, s.key, data, opts)
	if err != nil {
		if errors.Is(err, objstore.ErrPreconditionFailed) {
			return VersionNone, fmt.Errorf("%w: %s", ErrConflict, s.Location())
		}
		return VersionNone, err
	}
	// Some providers omit ETag on PUT; fall back to a HEAD.
	if etag == "" {
		etag, err = s.client.Head(ctx, s.key)
		if err != nil {
			return VersionNone, err
		}
	}
	return Version(etag), nil
}

var _ Store = (*S3Store)(nil)

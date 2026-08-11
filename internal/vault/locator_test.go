package vault_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/vault"
)

func TestLocator_FileRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(vault.PathEnv, "")

	path := filepath.Join(t.TempDir(), "ownbase.kdbx")
	loc := vault.Locator{Kind: vault.LocatorKindFile, Path: path}
	if err := vault.SaveLocator(loc); err != nil {
		t.Fatal(err)
	}
	got, err := vault.LoadLocator()
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != vault.LocatorKindFile || got.Path != path {
		t.Errorf("LoadLocator = %+v, want file %s", got, path)
	}
	// Legacy pointer kept in sync.
	resolved, err := vault.ResolvePath()
	if err != nil || resolved != path {
		t.Errorf("ResolvePath = %q %v, want %q", resolved, err, path)
	}
}

func TestLocator_LegacyPointerFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(vault.PathEnv, "")

	path := filepath.Join(t.TempDir(), "legacy.kdbx")
	if _, err := vault.RecordPath(path); err != nil {
		t.Fatal(err)
	}
	got, err := vault.LoadLocator()
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != vault.LocatorKindFile || got.Path != path {
		t.Errorf("legacy fallback = %+v", got)
	}
}

func TestRecoveryString_RoundTrip(t *testing.T) {
	loc := vault.Locator{
		Kind:            vault.LocatorKindS3,
		Endpoint:        "https://example.r2.cloudflarestorage.com",
		Region:          "auto",
		Bucket:          "vault-bucket",
		Key:             vault.DefaultObjectKey,
		AccessKeyID:     "AKIAEXAMPLE",
		SecretAccessKey: "secret-value-here",
	}
	s, err := vault.EncodeRecovery(loc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, "ownbase-recovery-v1:") {
		t.Errorf("prefix: %s", s)
	}
	got, err := vault.DecodeRecovery(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bucket != loc.Bucket || got.SecretAccessKey != loc.SecretAccessKey || got.Key != loc.Key {
		t.Errorf("decoded = %+v", got)
	}
}

func TestRecoveryString_ChecksumDetectsTypo(t *testing.T) {
	loc := vault.Locator{
		Kind:            vault.LocatorKindS3,
		Region:          "us-east-1",
		Bucket:          "b",
		Key:             "k",
		AccessKeyID:     "a",
		SecretAccessKey: "s",
	}
	s, err := vault.EncodeRecovery(loc)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a character in the payload.
	corrupted := s[:len(s)-2] + "xx"
	if _, err := vault.DecodeRecovery(corrupted); err == nil {
		t.Fatal("expected checksum error")
	}
}

func TestFingerprint_ChangesWithSecret(t *testing.T) {
	a := vault.Locator{
		Kind: vault.LocatorKindS3, Region: "auto", Bucket: "b", Key: "k",
		AccessKeyID: "id", SecretAccessKey: "one",
	}
	b := a
	b.SecretAccessKey = "two"
	if a.Fingerprint() == b.Fingerprint() {
		t.Error("fingerprint should change when secret changes")
	}
	if a.Fingerprint() == "" {
		t.Error("empty fingerprint")
	}
}

func TestCachingStore_Fallback(t *testing.T) {
	inner := vault.NewMemStore("remote")
	cacheDir := t.TempDir()
	c := vault.NewCachingStore(inner, cacheDir)
	ctx := context.Background()

	// Populate via put.
	if _, err := c.Put(ctx, []byte("cipher"), vault.VersionNone); err != nil {
		t.Fatal(err)
	}
	// Break the inner store by forcing it empty without clearing cache:
	// replace with a store that always errors.
	broken := &errStore{err: errors.New("network down")}
	c2 := vault.NewCachingStore(broken, cacheDir)
	// Same location id requires same Location() string.
	// errStore returns "remote" to match.
	data, _, err := c2.Get(ctx)
	if err != nil {
		// Cache key is hash of Location(); broken must share Location.
		t.Fatalf("cache fallback: %v (warn=%q)", err, c2.LastWarn())
	}
	if string(data) != "cipher" {
		t.Errorf("cached data = %q", data)
	}
	if c2.LastWarn() == "" {
		t.Error("expected fallback warning")
	}
}

// errStore fails every Get; Put is unused.
type errStore struct{ err error }

func (e *errStore) Get(context.Context) ([]byte, vault.Version, error) {
	return nil, vault.VersionNone, e.err
}
func (e *errStore) Put(context.Context, []byte, vault.Version) (vault.Version, error) {
	return vault.VersionNone, e.err
}
func (e *errStore) Location() string { return "remote" }

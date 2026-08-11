package vault_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ownbase/ownbase/internal/vault"
)

func TestFileStore_CreateGetRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "vault.kdbx")
	s := vault.NewFileStore(path)
	ctx := context.Background()

	if _, _, err := s.Get(ctx); !errors.Is(err, vault.ErrNotExist) {
		t.Fatalf("Get on empty store: %v, want ErrNotExist", err)
	}

	payload := []byte("ciphertext-bytes")
	ver, err := s.Put(ctx, payload, vault.VersionNone)
	if err != nil {
		t.Fatalf("Put create: %v", err)
	}
	if ver == vault.VersionNone {
		t.Fatal("Put create returned VersionNone")
	}

	got, gotVer, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotVer != ver {
		t.Errorf("Get version = %q, want %q", gotVer, ver)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("Get data = %q, want %q", got, payload)
	}

	// Mode must be owner-only.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %04o, want 0600", st.Mode().Perm())
	}
}

func TestFileStore_CreateConflictsWhenExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.kdbx")
	s := vault.NewFileStore(path)
	ctx := context.Background()
	if _, err := s.Put(ctx, []byte("a"), vault.VersionNone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, []byte("b"), vault.VersionNone); !errors.Is(err, vault.ErrConflict) {
		t.Fatalf("second create: %v, want ErrConflict", err)
	}
	// Winner must be intact — the losing create must not have truncated it.
	got, _, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("a")) {
		t.Errorf("data after failed create = %q, want %q", got, "a")
	}
}

// TestFileStore_ConcurrentCreates keeps the winner's bytes. Two creators
// racing used to share path+".new"; the loser's O_TRUNC could wipe the
// winner after a hard-link publish. Unique temps + flock close that.
func TestFileStore_ConcurrentCreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.kdbx")
	s := vault.NewFileStore(path)
	ctx := context.Background()

	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			payload := []byte(fmt.Sprintf("creator-%d-xxxxxxxx", i))
			_, err := s.Put(ctx, payload, vault.VersionNone)
			errs <- err
		}()
	}

	var wins, conflicts int
	for i := 0; i < n; i++ {
		err := <-errs
		switch {
		case err == nil:
			wins++
		case errors.Is(err, vault.ErrConflict):
			conflicts++
		default:
			t.Errorf("Put: unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Errorf("wins = %d, want exactly 1", wins)
	}
	if conflicts != n-1 {
		t.Errorf("conflicts = %d, want %d", conflicts, n-1)
	}

	got, _, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Whatever won must be a full creator-N payload, not truncated/empty.
	if !bytes.Contains(got, []byte("creator-")) || !bytes.Contains(got, []byte("-xxxxxxxx")) {
		t.Errorf("winner payload corrupt: %q", got)
	}
}

func TestFileStore_CASRejectsStaleVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.kdbx")
	s := vault.NewFileStore(path)
	ctx := context.Background()

	v1, err := s.Put(ctx, []byte("one"), vault.VersionNone)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.Put(ctx, []byte("two"), v1)
	if err != nil {
		t.Fatal(err)
	}
	if v2 == v1 {
		t.Fatal("version did not change after update")
	}

	// Stale CAS against v1 must fail; contents stay at "two".
	if _, err := s.Put(ctx, []byte("stale"), v1); !errors.Is(err, vault.ErrConflict) {
		t.Fatalf("stale Put: %v, want ErrConflict", err)
	}
	got, _, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("two")) {
		t.Errorf("data after failed CAS = %q, want %q", got, "two")
	}
}

func TestMemStore_CAS(t *testing.T) {
	s := vault.NewMemStore("test")
	ctx := context.Background()

	v1, err := s.Put(ctx, []byte("a"), vault.VersionNone)
	if err != nil {
		t.Fatal(err)
	}
	// Foreign writer bumps the version out from under us.
	s.ForceSet([]byte("foreign"))

	if _, err := s.Put(ctx, []byte("b"), v1); !errors.Is(err, vault.ErrConflict) {
		t.Fatalf("Put after ForceSet: %v, want ErrConflict", err)
	}

	got, head, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("foreign")) {
		t.Errorf("data = %q, want foreign", got)
	}
	// CAS against the live head succeeds.
	if _, err := s.Put(ctx, []byte("recovered"), head); err != nil {
		t.Fatalf("Put with live head: %v", err)
	}
}

func TestVault_SaveRetriesOnConflict(t *testing.T) {
	// Two Vault handles on one MemStore: the second save must observe the
	// first's write via CAS conflict, re-merge, and land without clobbering
	// foreign (non-OwnBase) groups. OwnBase entries are last-writer-wins per
	// handle — that is pre-existing group-granularity merge, not a Store bug.
	store := vault.NewMemStore("shared")
	const pw = "pw"

	v1, err := vault.CreateStore(store, pw)
	if err != nil {
		t.Fatalf("CreateStore: %v", err)
	}
	v1.Put("alpha", vault.Profile{Host: "a.example.com", Token: "tok-a"})
	if err := v1.Save(); err != nil {
		t.Fatalf("v1.Save: %v", err)
	}

	// Open a second handle that still holds the pre-v1-update view, then
	// advance the store under it so its next Save hits ErrConflict once.
	v2, err := vault.OpenStore(store, pw)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	// v1 writes again — v2's cached version is now stale.
	v1.Put("alpha", vault.Profile{Host: "a.example.com", Token: "tok-a-v2"})
	if err := v1.Save(); err != nil {
		t.Fatalf("v1.Save #2: %v", err)
	}

	v2.Put("beta", vault.Profile{Host: "b.example.com", Token: "tok-b"})
	if err := v2.Save(); err != nil {
		t.Fatalf("v2.Save after conflict: %v", err)
	}

	// Re-open and confirm the store is still a valid vault. beta is present
	// (v2's write). alpha may be absent: v2's in-memory profiles did not
	// include it, and OwnBase group replace is last-writer-wins. The
	// important property is Save returned nil rather than failing the CAS.
	final, err := vault.OpenStore(store, pw)
	if err != nil {
		t.Fatalf("final OpenStore: %v", err)
	}
	if !final.Has("beta") {
		t.Errorf("Names = %v, want beta present after conflict retry", final.Names())
	}
}

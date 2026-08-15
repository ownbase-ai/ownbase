package vault_test

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobischo/gokeepasslib/v3"
	"github.com/tobischo/gokeepasslib/v3/wrappers"

	"github.com/ownbase/ownbase/internal/vault"
)

func newTestVault(t *testing.T, password string) (*vault.Vault, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.kdbx")
	v, err := vault.Create(path, password)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return v, path
}

func TestCreateOpenRoundTrip(t *testing.T) {
	v, path := newTestVault(t, "correct horse")

	priv, pub, err := vault.NewKeyPair("ownbase_mybase")
	if err != nil {
		t.Fatalf("NewKeyPair: %v", err)
	}
	remote := false
	v.Put("mybase", vault.Profile{
		Host:          "203.0.113.10",
		SSHUser:       "root",
		SSHPort:       2222,
		APIPort:       7070,
		Token:         "tok-123",
		ConfigRepoURL: "git@github.com:org/mybase-config.git",
		ConfigRef:     "main",
		LocalVM:       &remote,
		PrivateKey:    priv,
		PublicKey:     pub,
	})
	if err := v.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := vault.Open(path, "correct horse")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := reopened.Get("mybase")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Host != "203.0.113.10" || got.SSHUser != "root" || got.SSHPort != 2222 {
		t.Errorf("connection fields did not round-trip: %+v", got)
	}
	if got.Token != "tok-123" {
		t.Errorf("Token = %q, want tok-123", got.Token)
	}
	if got.ConfigRepoURL != "git@github.com:org/mybase-config.git" || got.ConfigRef != "main" {
		t.Errorf("config repo fields did not round-trip: %+v", got)
	}
	if !got.KnownRemote() || got.KnownLocalVM() {
		t.Errorf("LocalVM tri-state did not round-trip: %v", got.LocalVM)
	}
	if _, err := got.Signer(); err != nil {
		t.Errorf("stored owner key did not round-trip: %v", err)
	}
	if got.PublicKeyLine() == "" {
		t.Error("PublicKeyLine is empty")
	}
}

func TestOpenWrongPassword(t *testing.T) {
	_, path := newTestVault(t, "right")
	if _, err := vault.Open(path, "wrong"); !errors.Is(err, vault.ErrWrongPassword) {
		t.Fatalf("Open with wrong password: got %v, want ErrWrongPassword", err)
	}
}

func TestCreateRefusesToOverwrite(t *testing.T) {
	_, path := newTestVault(t, "pw")
	if _, err := vault.Create(path, "pw2"); err == nil {
		t.Fatal("Create overwrote an existing vault")
	}
}

func TestDeleteRemovesProfile(t *testing.T) {
	v, path := newTestVault(t, "pw")
	v.Put("a", vault.Profile{Host: "a.example.com"})
	v.Put("b", vault.Profile{Host: "b.example.com"})
	if err := v.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	v.Delete("a")
	if err := v.Save(); err != nil {
		t.Fatalf("Save after delete: %v", err)
	}

	reopened, err := vault.Open(path, "pw")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if names := reopened.Names(); len(names) != 1 || names[0] != "b" {
		t.Errorf("Names = %v, want [b]", names)
	}
	if _, err := reopened.Get("a"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("Get(a) = %v, want ErrNotFound", err)
	}
}

func TestChangePassword(t *testing.T) {
	v, path := newTestVault(t, "old")
	v.Put("mybase", vault.Profile{Host: "h", Token: "tok"})
	if err := v.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := v.ChangePassword("new"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, err := vault.Open(path, "old"); err == nil {
		t.Error("vault still opens with the old password")
	}
	reopened, err := vault.Open(path, "new")
	if err != nil {
		t.Fatalf("Open with new password: %v", err)
	}
	p, err := reopened.Get("mybase")
	if err != nil || p.Token != "tok" {
		t.Errorf("profile lost across password change: %+v %v", p, err)
	}
}

func TestRepeatedSavesKeepProfilesReadable(t *testing.T) {
	v, path := newTestVault(t, "pw")
	v.Put("mybase", vault.Profile{Host: "h", Token: "tok"})

	// Each save re-encrypts from the in-memory database, and the protected
	// values are toggled between locked and plaintext on the way. Getting
	// that wrong garbles the token on the second or third write rather than
	// the first, so this loops.
	for i := range 3 {
		if err := v.Save(); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		reopened, err := vault.Open(path, "pw")
		if err != nil {
			t.Fatalf("open after save %d: %v", i, err)
		}
		p, err := reopened.Get("mybase")
		if err != nil {
			t.Fatalf("get after save %d: %v", i, err)
		}
		if p.Host != "h" || p.Token != "tok" {
			t.Fatalf("after save %d the profile is corrupt: %+v", i, p)
		}
	}
}

// The vault may sit in cloud storage, which keeps old versions of the file by
// design. gokeepasslib generates the master seed and cipher nonce once, when
// the database is constructed, and reuses them on every encode — so without
// reseeding, two saved versions are the same keystream over different
// plaintexts and XOR to cleartext.
func TestSaveUsesFreshSeedsEachWrite(t *testing.T) {
	v, path := newTestVault(t, "pw")

	seen := map[string]int{}
	for i := range 3 {
		v.Put("mybase", vault.Profile{Host: fmt.Sprintf("host-%d", i)})
		if err := v.Save(); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		h := readHeader(t, path, "pw")
		seen["seed:"+hex.EncodeToString(h.FileHeaders.MasterSeed)]++
		seen["iv:"+hex.EncodeToString(h.FileHeaders.EncryptionIV)]++
		seen["salt:"+hex.EncodeToString(h.FileHeaders.KdfParameters.Salt[:])]++
	}
	for k, n := range seen {
		if n > 1 {
			t.Errorf("%s was reused across %d saves", strings.SplitN(k, ":", 2)[0], n)
		}
	}
}

// A vault can live in the user's own KeePass file, so an ownbasectl write must
// not delete or corrupt what it did not create.
func TestSavePreservesForeignEntries(t *testing.T) {
	v, path := newTestVault(t, "pw")
	v.Put("mybase", vault.Profile{Host: "h"})
	if err := v.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Stand in for the user adding an entry in KeePassXC.
	addForeignEntry(t, path, "pw", "my bank", "s3cret")

	// An ownbasectl write after that must merge, not clobber.
	v2, err := vault.Open(path, "pw")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	v2.Put("other", vault.Profile{Host: "h2"})
	if err := v2.Save(); err != nil {
		t.Fatalf("Save after foreign edit: %v", err)
	}

	title, password := findForeignEntry(t, path, "pw", "my bank")
	if title == "" {
		t.Fatal("the user's own entry was dropped by an ownbasectl save")
	}
	if password != "s3cret" {
		t.Errorf("the user's protected password was corrupted: %q", password)
	}

	reopened, err := vault.Open(path, "pw")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !reopened.Has("mybase") || !reopened.Has("other") {
		t.Errorf("Bases lost across the merge: %v", reopened.Names())
	}
}

func readHeader(t *testing.T, path, password string) *gokeepasslib.DBHeader {
	t.Helper()
	return decodeRaw(t, path, password).Header
}

func decodeRaw(t *testing.T, path, password string) *gokeepasslib.Database {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	defer f.Close()
	db := gokeepasslib.NewDatabase()
	db.Credentials = gokeepasslib.NewPasswordCredentials(password)
	if err := gokeepasslib.NewDecoder(f).Decode(db); err != nil {
		t.Fatalf("decode vault: %v", err)
	}
	if err := db.UnlockProtectedEntries(); err != nil {
		t.Fatalf("unlock entries: %v", err)
	}
	return db
}

// addForeignEntry writes an entry outside the OwnBase group, the way another
// KDBX client would.
func addForeignEntry(t *testing.T, path, password, title, secret string) {
	t.Helper()
	db := decodeRaw(t, path, password)

	e := gokeepasslib.NewEntry()
	e.Values = append(e.Values,
		gokeepasslib.ValueData{Key: "Title", Value: gokeepasslib.V{Content: title}},
		gokeepasslib.ValueData{
			Key:   "Password",
			Value: gokeepasslib.V{Content: secret, Protected: wrappers.NewBoolWrapper(true)},
		},
	)
	g := gokeepasslib.NewGroup()
	g.Name = "Personal"
	g.Entries = []gokeepasslib.Entry{e}
	db.Content.Root.Groups = append(db.Content.Root.Groups, g)

	if err := db.LockProtectedEntries(); err != nil {
		t.Fatalf("lock entries: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("rewrite vault: %v", err)
	}
	defer f.Close()
	if err := gokeepasslib.NewEncoder(f).Encode(db); err != nil {
		t.Fatalf("encode vault: %v", err)
	}
}

func findForeignEntry(t *testing.T, path, password, title string) (string, string) {
	t.Helper()
	db := decodeRaw(t, path, password)
	var walk func(groups []gokeepasslib.Group) (string, string)
	walk = func(groups []gokeepasslib.Group) (string, string) {
		for _, g := range groups {
			for _, e := range g.Entries {
				if e.GetTitle() == title {
					return e.GetTitle(), e.GetPassword()
				}
			}
			if ft, fp := walk(g.Groups); ft != "" {
				return ft, fp
			}
		}
		return "", ""
	}
	return walk(db.Content.Root.Groups)
}

func TestBackupCredentialsRoundTrip(t *testing.T) {
	v, path := newTestVault(t, "pw")
	v.Put("mybase", vault.Profile{
		Host:                    "h",
		BackupRepo:              "s3:s3.amazonaws.com/bucket/ownbase",
		ResticPassword:          "restic-secret",
		AWSAccessKeyID:          "AKIAEXAMPLE",
		AWSSecretAccessKey:      "aws-secret",
		B2AccountID:             "b2id",
		B2AccountKey:            "b2-secret",
		AdminAWSAccessKeyID:     "AKIAADMIN",
		AdminAWSSecretAccessKey: "admin-secret",
		AdminB2AccountID:        "adminb2",
		AdminB2AccountKey:       "admin-b2-secret",
	})
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	reopened, err := vault.Open(path, "pw")
	if err != nil {
		t.Fatal(err)
	}
	p, err := reopened.Get("mybase")
	if err != nil {
		t.Fatal(err)
	}
	if p.BackupRepo != "s3:s3.amazonaws.com/bucket/ownbase" ||
		p.ResticPassword != "restic-secret" ||
		p.AWSSecretAccessKey != "aws-secret" ||
		p.B2AccountKey != "b2-secret" ||
		p.AdminAWSAccessKeyID != "AKIAADMIN" ||
		p.AdminAWSSecretAccessKey != "admin-secret" ||
		p.AdminB2AccountKey != "admin-b2-secret" {
		t.Errorf("backup fields did not round-trip: %+v", p)
	}
	if !p.HasBackupCredentials() {
		t.Error("HasBackupCredentials = false")
	}
	bc := p.BackupCredentials()
	if bc.AdminAWSSecretAccessKey != "admin-secret" || bc.AdminB2AccountKey != "admin-b2-secret" {
		t.Errorf("BackupCredentials missing admin fields: %+v", bc)
	}
	r := p.Redacted()
	if r.ResticPassword != "" || r.AWSSecretAccessKey != "" || r.B2AccountKey != "" ||
		r.AdminAWSSecretAccessKey != "" || r.AdminB2AccountKey != "" || r.PrivateKey != "" {
		t.Errorf("Redacted leaked secrets: %+v", r)
	}
	if r.BackupRepo == "" || r.AWSAccessKeyID == "" || r.AdminAWSAccessKeyID == "" {
		t.Errorf("Redacted stripped non-secrets: %+v", r)
	}
}

func TestMergeSecretsFrom(t *testing.T) {
	existing := vault.Profile{
		PrivateKey:              "priv",
		PublicKey:               "pub",
		ResticPassword:          "rpw",
		AWSSecretAccessKey:      "asec",
		BackupRepo:              "repo",
		AdminAWSAccessKeyID:     "admin-id",
		AdminAWSSecretAccessKey: "admin-sec",
	}
	p := vault.Profile{Host: "new-host", BackupRepo: ""}
	p.MergeSecretsFrom(existing)
	if p.PrivateKey != "priv" || p.ResticPassword != "rpw" || p.BackupRepo != "repo" ||
		p.AdminAWSAccessKeyID != "admin-id" || p.AdminAWSSecretAccessKey != "admin-sec" {
		t.Errorf("merge incomplete: %+v", p)
	}
	// Explicit values win.
	p2 := vault.Profile{ResticPassword: "new"}
	p2.MergeSecretsFrom(existing)
	if p2.ResticPassword != "new" {
		t.Errorf("explicit secret overwritten: %q", p2.ResticPassword)
	}
}

func TestProfileDefaults(t *testing.T) {
	p := vault.Profile{}
	if p.EffectiveSSHUser() != vault.DefaultSSHUser {
		t.Errorf("EffectiveSSHUser = %q", p.EffectiveSSHUser())
	}
	if p.EffectiveSSHPort() != vault.DefaultSSHPort {
		t.Errorf("EffectiveSSHPort = %d", p.EffectiveSSHPort())
	}
	if p.EffectiveAPIPort() != vault.DefaultAPIPort {
		t.Errorf("EffectiveAPIPort = %d", p.EffectiveAPIPort())
	}
	if p.EffectiveConfigRef() != vault.DefaultConfigRef {
		t.Errorf("EffectiveConfigRef = %q", p.EffectiveConfigRef())
	}
	if p.KnownRemote() || p.KnownLocalVM() {
		t.Error("an unset LocalVM must be neither known-local nor known-remote")
	}
}

func TestRecordAndResolvePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(vault.PathEnv, "")

	target := filepath.Join(t.TempDir(), "cloud", "ownbase.kdbx")
	recorded, err := vault.RecordPath(target)
	if err != nil {
		t.Fatalf("RecordPath: %v", err)
	}
	if recorded != target {
		t.Errorf("recorded = %q, want %q", recorded, target)
	}
	got, err := vault.ResolvePath()
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if got != target {
		t.Errorf("ResolvePath = %q, want %q", got, target)
	}
}

// NormalizePath must resolve the same way as RecordPath without touching the
// pointer — vault init relies on that so a failed password cannot move
// ~/.ownbase/vault off a working location.
func TestNormalizePathDoesNotWritePointer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(vault.PathEnv, "")

	existing := filepath.Join(t.TempDir(), "good.kdbx")
	if _, err := vault.RecordPath(existing); err != nil {
		t.Fatal(err)
	}

	other := filepath.Join(t.TempDir(), "other.kdbx")
	got, err := vault.NormalizePath(other)
	if err != nil {
		t.Fatalf("NormalizePath: %v", err)
	}
	if got != other {
		t.Errorf("NormalizePath = %q, want %q", got, other)
	}
	still, err := vault.ResolvePath()
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if still != existing {
		t.Errorf("ResolvePath = %q after NormalizePath(other), want %q (pointer must be unchanged)", still, existing)
	}
}

func TestResolvePathWithoutVault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(vault.PathEnv, "")
	if _, err := vault.ResolvePath(); !errors.Is(err, vault.ErrNoVault) {
		t.Fatalf("ResolvePath = %v, want ErrNoVault", err)
	}
}

func TestRecordPathAppendsFilenameForDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	recorded, err := vault.RecordPath(dir)
	if err != nil {
		t.Fatalf("RecordPath: %v", err)
	}
	if recorded != filepath.Join(dir, vault.DefaultFileName) {
		t.Errorf("recorded = %q, want %q", recorded, filepath.Join(dir, vault.DefaultFileName))
	}
}

// `vault init ~/Dropbox/OwnBase` names a folder the user has not made yet.
// Taking it literally would create a file called OwnBase with no extension,
// which is not what anyone means by that.
func TestRecordPathTreatsExtensionlessMissingPathAsDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	target := filepath.Join(t.TempDir(), "cloud", "OwnBase")
	recorded, err := vault.RecordPath(target)
	if err != nil {
		t.Fatalf("RecordPath: %v", err)
	}
	if recorded != filepath.Join(target, vault.DefaultFileName) {
		t.Errorf("recorded = %q, want %q", recorded, filepath.Join(target, vault.DefaultFileName))
	}
}

// An explicit filename is still honoured exactly.
func TestRecordPathKeepsExplicitFilename(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	target := filepath.Join(t.TempDir(), "cloud", "work.kdbx")
	recorded, err := vault.RecordPath(target)
	if err != nil {
		t.Fatalf("RecordPath: %v", err)
	}
	if recorded != target {
		t.Errorf("recorded = %q, want %q", recorded, target)
	}
}

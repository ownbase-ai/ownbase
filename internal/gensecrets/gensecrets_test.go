package gensecrets_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"golang.org/x/crypto/ssh"

	"github.com/ownbase/ownbase/internal/gensecrets"
	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/secrets"
)

// testStore is a temporary secrets directory plus its age key.
type testStore struct {
	dir  string
	opts gensecrets.Config
}

func newTestStore(t *testing.T) testStore {
	t.Helper()
	root := t.TempDir()
	keyPath := filepath.Join(root, "key.age")
	if _, err := secrets.GenerateAndSave(keyPath); err != nil {
		t.Fatalf("generate age key: %v", err)
	}
	dir := filepath.Join(root, "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	return testStore{
		dir:  dir,
		opts: gensecrets.Config{SecretsDir: dir, AgeKeyPath: keyPath},
	}
}

func (s testStore) read(t *testing.T, service string) map[string]string {
	t.Helper()
	vals, err := secrets.IssueMap(secrets.FileKeyCustody{Path: s.opts.AgeKeyPath},
		filepath.Join(s.dir, service+".yaml.age"))
	if err != nil {
		t.Fatalf("read secrets for %s: %v", service, err)
	}
	return vals
}

func (s testStore) write(t *testing.T, service string, vals map[string]string) {
	t.Helper()
	data, err := os.ReadFile(s.opts.AgeKeyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	id, err := age.ParseX25519Identity(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	ciphertext, err := secrets.EncryptSecrets(id.Recipient(), vals)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, service+".yaml.age"), ciphertext, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// pgBackRestLikeConfig mirrors the scaffolded Postgres + pgBackRest pair: a
// split keypair and a password.
func pgBackRestLikeConfig() *schema.OwnbaseConfig {
	return &schema.OwnbaseConfig{
		SchemaVersion: schema.CurrentSchemaVersion,
		Services: map[string]schema.ServiceDecl{
			"pgbackrest": {
				Repo: "https://github.com/ownbase-ai/pgbackrest",
				GeneratedSecrets: []schema.GeneratedSecretDecl{{
					Type:            schema.GeneratedSecretSSHEd25519,
					PublicKey:       "PGBACKREST_CLIENT_PUBKEY",
					PrivateKey:      "postgres:PGBACKREST_SSH_KEY_B64",
					PrivateEncoding: "base64",
				}},
			},
			"postgres": {
				Repo: "https://github.com/ownbase-ai/pgbackrest",
				GeneratedSecrets: []schema.GeneratedSecretDecl{{
					Type: schema.GeneratedSecretPassword,
					Key:  "POSTGRES_PASSWORD",
				}},
			},
		},
	}
}

func TestEnsure_GeneratesKeypairAcrossServicesAndPassword(t *testing.T) {
	store := newTestStore(t)

	res, err := gensecrets.Ensure(pgBackRestLikeConfig(), store.opts)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	want := []string{"pgbackrest/PGBACKREST_CLIENT_PUBKEY", "postgres/PGBACKREST_SSH_KEY_B64", "postgres/POSTGRES_PASSWORD"}
	if got := strings.Join(res.Generated, ","); got != strings.Join(want, ",") {
		t.Errorf("Generated = %v, want %v", res.Generated, want)
	}

	repoVals := store.read(t, "pgbackrest")
	pgVals := store.read(t, "postgres")

	pub := repoVals["PGBACKREST_CLIENT_PUBKEY"]
	if !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Errorf("public key = %q, want an ssh-ed25519 authorized_keys line", pub)
	}

	// The private half must be a real key that matches the public half, or
	// pgBackRest's SSH connection fails in a way no log makes obvious.
	privB64 := pgVals["PGBACKREST_SSH_KEY_B64"]
	pemBytes, err := base64.StdEncoding.DecodeString(privB64)
	if err != nil {
		t.Fatalf("private key is not valid base64: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(pemBytes)
	if err != nil {
		t.Fatalf("private key does not parse: %v", err)
	}
	parsedPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		t.Fatalf("public key does not parse: %v", err)
	}
	if string(parsedPub.Marshal()) != string(signer.PublicKey().Marshal()) {
		t.Error("generated public key does not match the generated private key")
	}

	if got := pgVals["POSTGRES_PASSWORD"]; len(got) != schema.DefaultGeneratedPasswordLength {
		t.Errorf("password length = %d, want %d", len(got), schema.DefaultGeneratedPasswordLength)
	}
}

func TestEnsure_IsIdempotent(t *testing.T) {
	store := newTestStore(t)
	cfg := pgBackRestLikeConfig()

	if _, err := gensecrets.Ensure(cfg, store.opts); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	first := store.read(t, "postgres")

	res, err := gensecrets.Ensure(cfg, store.opts)
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if len(res.Generated) != 0 {
		t.Errorf("second Ensure generated %v, want nothing", res.Generated)
	}
	if got := store.read(t, "postgres"); got["POSTGRES_PASSWORD"] != first["POSTGRES_PASSWORD"] {
		t.Error("second Ensure rotated an existing password")
	}
}

func TestEnsure_PreservesUnrelatedSecrets(t *testing.T) {
	store := newTestStore(t)
	store.write(t, "postgres", map[string]string{"SOMETHING_ELSE": "keep-me"})

	if _, err := gensecrets.Ensure(pgBackRestLikeConfig(), store.opts); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	vals := store.read(t, "postgres")
	if vals["SOMETHING_ELSE"] != "keep-me" {
		t.Errorf("SOMETHING_ELSE = %q, want it left alone", vals["SOMETHING_ELSE"])
	}
	if vals["POSTGRES_PASSWORD"] == "" {
		t.Error("POSTGRES_PASSWORD was not generated alongside the existing key")
	}
}

// An operator-supplied value must win. Regenerating over it would break
// whatever they configured to match.
func TestEnsure_LeavesOperatorSuppliedValueAlone(t *testing.T) {
	store := newTestStore(t)
	store.write(t, "postgres", map[string]string{"POSTGRES_PASSWORD": "chosen-by-hand"})

	if _, err := gensecrets.Ensure(pgBackRestLikeConfig(), store.opts); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := store.read(t, "postgres")["POSTGRES_PASSWORD"]; got != "chosen-by-hand" {
		t.Errorf("POSTGRES_PASSWORD = %q, want %q", got, "chosen-by-hand")
	}
}

// A keypair is only useful if both halves come from the same generation. If one
// half is already present, regenerating the other would produce a mismatched
// pair that authenticates against nothing.
func TestEnsure_SkipsHalfPresentKeypair(t *testing.T) {
	store := newTestStore(t)
	store.write(t, "pgbackrest", map[string]string{"PGBACKREST_CLIENT_PUBKEY": "ssh-ed25519 AAAA existing"})

	if _, err := gensecrets.Ensure(pgBackRestLikeConfig(), store.opts); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if got := store.read(t, "pgbackrest")["PGBACKREST_CLIENT_PUBKEY"]; got != "ssh-ed25519 AAAA existing" {
		t.Errorf("existing public key was replaced with %q", got)
	}
	if got := store.read(t, "postgres")["PGBACKREST_SSH_KEY_B64"]; got != "" {
		t.Error("generated a private key that cannot match the existing public key")
	}
}

// A Base declaring no generated secrets must not need an age key at all.
func TestEnsure_NoDeclarationsNeedsNoKey(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: schema.CurrentSchemaVersion,
		Services: map[string]schema.ServiceDecl{
			"api": {Repo: "https://github.com/org/api"},
		},
	}
	res, err := gensecrets.Ensure(cfg, gensecrets.Config{
		SecretsDir: filepath.Join(t.TempDir(), "nope"),
		AgeKeyPath: filepath.Join(t.TempDir(), "missing.age"),
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(res.Generated) != 0 {
		t.Errorf("Generated = %v, want nothing", res.Generated)
	}
}

func TestEnsure_RawPrivateEncodingStoresPEM(t *testing.T) {
	store := newTestStore(t)
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: schema.CurrentSchemaVersion,
		Services: map[string]schema.ServiceDecl{
			"repohost": {
				Repo: "https://github.com/org/repohost",
				GeneratedSecrets: []schema.GeneratedSecretDecl{{
					Type:       schema.GeneratedSecretSSHEd25519,
					PublicKey:  "PUB",
					PrivateKey: "PRIV",
				}},
			},
		},
	}
	if _, err := gensecrets.Ensure(cfg, store.opts); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	priv := store.read(t, "repohost")["PRIV"]
	if !strings.HasPrefix(priv, "-----BEGIN OPENSSH PRIVATE KEY-----") {
		t.Error("private key is not a PEM block")
	}
}

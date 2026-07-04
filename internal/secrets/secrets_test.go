package secrets_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/ownbase/ownbase/internal/secrets"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func generateTestIdentity(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate test identity: %v", err)
	}
	return id
}

// writeKeyFile writes an age private key to a temp file with 0600 permissions
// and returns a FileKeyCustody pointed at it.
func writeKeyFile(t *testing.T, id *age.X25519Identity) secrets.FileKeyCustody {
	t.Helper()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.age")
	if err := os.WriteFile(keyPath, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	return secrets.FileKeyCustody{Path: keyPath}
}

// writeEncryptedSecretsAt encrypts data for id.Recipient() and writes it to path.
func writeEncryptedSecretsAt(t *testing.T, id *age.X25519Identity, path string, data map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ciphertext, err := secrets.EncryptSecrets(id.Recipient(), data)
	if err != nil {
		t.Fatalf("encrypt secrets: %v", err)
	}
	if err := os.WriteFile(path, ciphertext, 0o600); err != nil {
		t.Fatalf("write encrypted secrets: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Round-trip: Issue decrypts everything from the file
// ---------------------------------------------------------------------------

func TestIssue_RoundTrip(t *testing.T) {
	id := generateTestIdentity(t)
	custody := writeKeyFile(t, id)

	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets", "crm.yaml.age")
	writeEncryptedSecretsAt(t, id, secretsPath, map[string]string{
		"DATABASE_URL": "postgres://user:pass@localhost/crm",
		"API_KEY":      "super-secret-api-key",
	})

	ss, err := secrets.Issue(custody, secretsPath)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	for _, tc := range []struct{ name, want string }{
		{"DATABASE_URL", "postgres://user:pass@localhost/crm"},
		{"API_KEY", "super-secret-api-key"},
	} {
		v, ok := ss.Get(tc.name)
		if !ok {
			t.Errorf("Get(%q): not found", tc.name)
			continue
		}
		if string(v) != tc.want {
			t.Errorf("Get(%q) = %q, want %q", tc.name, v, tc.want)
		}
	}

	if ss.Len() != 2 {
		t.Errorf("expected 2 secrets, got %d", ss.Len())
	}
}

// ---------------------------------------------------------------------------
// File is the isolation boundary: each service has its own file
// ---------------------------------------------------------------------------

func TestIssue_SeparateFilesIsolateBetweenServices(t *testing.T) {
	id := generateTestIdentity(t)
	custody := writeKeyFile(t, id)

	dir := t.TempDir()
	crmPath := filepath.Join(dir, "secrets", "crm.yaml.age")
	authPath := filepath.Join(dir, "secrets", "auth.yaml.age")

	writeEncryptedSecretsAt(t, id, crmPath, map[string]string{"CRM_DB_URL": "postgres://crm"})
	writeEncryptedSecretsAt(t, id, authPath, map[string]string{"AUTH_SECRET": "auth-only"})

	// Issue for crm's file — gets crm's secrets only.
	crmSet, err := secrets.Issue(custody, crmPath)
	if err != nil {
		t.Fatalf("Issue(crm): %v", err)
	}
	if _, ok := crmSet.Get("CRM_DB_URL"); !ok {
		t.Error("crm should have CRM_DB_URL")
	}
	if _, ok := crmSet.Get("AUTH_SECRET"); ok {
		t.Error("crm must not see AUTH_SECRET — it is in a different file")
	}

	// Issue for auth's file — gets auth's secrets only.
	authSet, err := secrets.Issue(custody, authPath)
	if err != nil {
		t.Fatalf("Issue(auth): %v", err)
	}
	if _, ok := authSet.Get("AUTH_SECRET"); !ok {
		t.Error("auth should have AUTH_SECRET")
	}
	if _, ok := authSet.Get("CRM_DB_URL"); ok {
		t.Error("auth must not see CRM_DB_URL — it is in a different file")
	}
}

// ---------------------------------------------------------------------------
// Secrets file can live anywhere (not just secrets/)
// ---------------------------------------------------------------------------

func TestIssue_SecretsFileCanBeAtArbitraryPath(t *testing.T) {
	id := generateTestIdentity(t)
	custody := writeKeyFile(t, id)

	dir := t.TempDir()
	// Secrets file alongside the app, not in a top-level secrets/ dir.
	secretsPath := filepath.Join(dir, "apps", "crm", "env.yaml.age")
	writeEncryptedSecretsAt(t, id, secretsPath, map[string]string{
		"KEY": "value",
	})

	ss, err := secrets.Issue(custody, secretsPath)
	if err != nil {
		t.Fatalf("Issue with non-conventional path: %v", err)
	}
	if v, ok := ss.Get("KEY"); !ok || string(v) != "value" {
		t.Errorf("unexpected Get(KEY): ok=%v val=%q", ok, v)
	}
}

// ---------------------------------------------------------------------------
// Empty path → empty set, no I/O
// ---------------------------------------------------------------------------

func TestIssue_EmptyPathReturnsEmptySetWithoutIO(t *testing.T) {
	id := generateTestIdentity(t)
	custody := writeKeyFile(t, id)

	// No file on disk at all — must not error with empty path.
	ss, err := secrets.Issue(custody, "")
	if err != nil {
		t.Fatalf("Issue with empty path: %v", err)
	}
	if ss.Len() != 0 {
		t.Errorf("expected empty set, got len %d", ss.Len())
	}
}

// ---------------------------------------------------------------------------
// Missing file → clear error
// ---------------------------------------------------------------------------

func TestIssue_MissingFileReturnsError(t *testing.T) {
	id := generateTestIdentity(t)
	custody := writeKeyFile(t, id)

	_, err := secrets.Issue(custody, "/nonexistent/secrets/missing.yaml.age")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// ---------------------------------------------------------------------------
// No plaintext exposed via GoString / fmt
// ---------------------------------------------------------------------------

func TestSecretSet_ValuesNotExposedInGoString(t *testing.T) {
	id := generateTestIdentity(t)
	custody := writeKeyFile(t, id)

	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets.yaml.age")
	writeEncryptedSecretsAt(t, id, secretsPath, map[string]string{
		"MY_SECRET": "ultra-secret-value-xyzzy",
	})

	ss, err := secrets.Issue(custody, secretsPath)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	rendered := fmt.Sprintf("%#v", ss)
	if strings.Contains(rendered, "ultra-secret-value-xyzzy") {
		t.Errorf("SecretSet GoString leaks plaintext value: %s", rendered)
	}
}

// ---------------------------------------------------------------------------
// FileKeyCustody: strict permission check
// ---------------------------------------------------------------------------

func TestFileKeyCustody_RefusesUnsafePermissions(t *testing.T) {
	id := generateTestIdentity(t)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.age")

	if err := os.WriteFile(keyPath, []byte(id.String()+"\n"), 0o644); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	custody := secrets.FileKeyCustody{Path: keyPath}
	_, err := custody.LoadIdentity()
	if err == nil {
		t.Fatal("expected error for key file with mode 0644, got nil")
	}
}

func TestFileKeyCustody_AcceptsMode0600(t *testing.T) {
	id := generateTestIdentity(t)
	custody := writeKeyFile(t, id)
	loaded, err := custody.LoadIdentity()
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	if loaded.Recipient().String() != id.Recipient().String() {
		t.Error("loaded identity has different public key")
	}
}

// ---------------------------------------------------------------------------
// GenerateAndSave
// ---------------------------------------------------------------------------

func TestGenerateAndSave_WritesValidKeyAt0600(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.age")

	id, err := secrets.GenerateAndSave(keyPath)
	if err != nil {
		t.Fatalf("GenerateAndSave: %v", err)
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if info.Mode()&0o777 != 0o600 {
		t.Errorf("key file mode = %04o, want 0600", info.Mode()&0o777)
	}

	custody := secrets.FileKeyCustody{Path: keyPath}
	loaded, err := custody.LoadIdentity()
	if err != nil {
		t.Fatalf("LoadIdentity after GenerateAndSave: %v", err)
	}
	if loaded.Recipient().String() != id.Recipient().String() {
		t.Error("loaded identity has different public key after GenerateAndSave")
	}
}

// ---------------------------------------------------------------------------
// NoopInjector records calls using the full file
// ---------------------------------------------------------------------------

func TestNoopInjector_RecordsCalls(t *testing.T) {
	id := generateTestIdentity(t)
	custody := writeKeyFile(t, id)

	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets", "crm.yaml.age")
	writeEncryptedSecretsAt(t, id, secretsPath, map[string]string{
		"DB_URL":  "postgres://crm",
		"API_KEY": "key",
	})

	ss, err := secrets.Issue(custody, secretsPath)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	inj := &secrets.NoopInjector{}
	if err := inj.Inject("crm", ss); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	if len(inj.Injected) != 2 {
		t.Errorf("expected 2 injected secrets, got %d: %v", len(inj.Injected), inj.Injected)
	}
	for _, want := range []string{"ownbase-crm-API_KEY", "ownbase-crm-DB_URL"} {
		if !containsStr(inj.Injected, want) {
			t.Errorf("expected %q in Injected, got %v", want, inj.Injected)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// IssueMap
// ---------------------------------------------------------------------------

func TestIssueMap_RoundTrip(t *testing.T) {
	id := generateTestIdentity(t)
	custody := writeKeyFile(t, id)

	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "backup.yaml.age")
	want := map[string]string{
		"AWS_ACCESS_KEY_ID":     "AKIAIOSFODNN7EXAMPLE",
		"AWS_SECRET_ACCESS_KEY": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"RESTIC_PASSWORD":       "hunter2",
	}
	writeEncryptedSecretsAt(t, id, secretsPath, want)

	got, err := secrets.IssueMap(custody, secretsPath)
	if err != nil {
		t.Fatalf("IssueMap: %v", err)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q: got %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d keys, want %d", len(got), len(want))
	}
}

func TestIssueMap_MissingFile_ReturnsEmpty(t *testing.T) {
	id := generateTestIdentity(t)
	custody := writeKeyFile(t, id)

	got, err := secrets.IssueMap(custody, filepath.Join(t.TempDir(), "nonexistent.yaml.age"))
	if err != nil {
		t.Fatalf("IssueMap missing file: expected nil error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestIssueMap_EmptyPath_ReturnsEmpty(t *testing.T) {
	id := generateTestIdentity(t)
	custody := writeKeyFile(t, id)

	got, err := secrets.IssueMap(custody, "")
	if err != nil {
		t.Fatalf("IssueMap empty path: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map for empty path, got %v", got)
	}
}

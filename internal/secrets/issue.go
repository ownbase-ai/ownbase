package secrets

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"

	"filippo.io/age"
	"gopkg.in/yaml.v3"
)

// SecretSet is an immutable collection of decrypted secret values for one
// container. The file is the isolation boundary: each repo should have its
// own dedicated secrets file so that its SecretSet contains exactly the
// secrets that service needs.
//
// Values are stored as []byte (not string) to reduce the chance of accidental
// logging via fmt.Sprintf or error messages. SecretSet does not implement
// fmt.Stringer, so printing one reveals only the key names, never values.
type SecretSet struct {
	values map[string][]byte
}

// Get returns the decrypted value for name, or (nil, false) if not present.
func (s SecretSet) Get(name string) ([]byte, bool) {
	v, ok := s.values[name]
	return v, ok
}

// Names returns the sorted list of secret names in this set.
func (s SecretSet) Names() []string {
	names := make([]string, 0, len(s.values))
	for n := range s.values {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Len returns the number of secrets in this set.
func (s SecretSet) Len() int { return len(s.values) }

// GoString renders key names but never values, guarding against accidental
// %#v exposure in logs or error output.
func (s SecretSet) GoString() string {
	return fmt.Sprintf("secrets.SecretSet{names: %v}", s.Names())
}

// Issue decrypts secretsFile and returns a SecretSet containing all
// key-value pairs in the file. The file boundary is the isolation unit:
// each repo should have its own dedicated secrets file.
//
// secretsFile is the path to the age-encrypted YAML file, resolved by the
// caller relative to the checkout root (e.g. filepath.Join(checkoutPath,
// container.SecretsFile)).
//
// If secretsFile is empty, Issue returns an empty SecretSet without touching
// the filesystem. This is the correct path for repos that declare no secrets.
func Issue(custody KeyCustody, secretsFile string) (SecretSet, error) {
	if secretsFile == "" {
		return SecretSet{values: map[string][]byte{}}, nil
	}

	id, err := custody.LoadIdentity()
	if err != nil {
		return SecretSet{}, fmt.Errorf("issue(%s): load key: %w", secretsFile, err)
	}

	ciphertext, err := os.ReadFile(secretsFile)
	if err != nil {
		return SecretSet{}, fmt.Errorf("issue(%s): read file: %w", secretsFile, err)
	}

	all, err := decryptYAML(id, ciphertext)
	if err != nil {
		return SecretSet{}, fmt.Errorf("issue(%s): decrypt: %w", secretsFile, err)
	}

	return SecretSet{values: all}, nil
}

// IssueMap decrypts secretsFile and returns the key-value pairs as a plain
// map[string]string for agent-internal use (not for Podman injection).
// Returns an empty map without error when the file does not exist — backups
// degrade cleanly in dev or on a fresh machine before the secret is set.
func IssueMap(custody KeyCustody, secretsFile string) (map[string]string, error) {
	if secretsFile == "" {
		return map[string]string{}, nil
	}

	id, err := custody.LoadIdentity()
	if err != nil {
		return nil, fmt.Errorf("IssueMap(%s): load key: %w", secretsFile, err)
	}

	ciphertext, err := os.ReadFile(secretsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("IssueMap(%s): read file: %w", secretsFile, err)
	}

	all, err := decryptYAML(id, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("IssueMap(%s): decrypt: %w", secretsFile, err)
	}

	out := make(map[string]string, len(all))
	for k, v := range all {
		out[k] = string(v)
	}
	return out, nil
}

// EncryptSecrets encrypts a flat map of secret key-value pairs for a recipient
// and returns the age-encrypted bytes. The resulting bytes can be written to
// any path — the agent resolves the path from the SecretsFile annotation in
// the compiled Quadlet unit.
//
// Usage:
//
//	ciphertext, _ := secrets.EncryptSecrets(id.Recipient(), map[string]string{
//	    "DATABASE_URL": "postgres://...",
//	    "API_KEY":      "sk-...",
//	})
//	os.WriteFile("secrets/crm.yaml.age", ciphertext, 0o600)
//
// The equivalent CLI workflow uses the age tool directly:
//
//	age --encrypt -r <pubkey> secrets.yaml > secrets/crm.yaml.age
func EncryptSecrets(recipient *age.X25519Recipient, data map[string]string) ([]byte, error) {
	raw, err := yaml.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal secrets YAML: %w", err)
	}
	return encryptForRecipient(recipient, raw)
}

// decryptYAML decrypts an age-encrypted YAML file and returns its key-value
// pairs. Values are []byte to prevent accidental string interpolation.
func decryptYAML(id *age.X25519Identity, ciphertext []byte) (map[string][]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ciphertext), id)
	if err != nil {
		return nil, fmt.Errorf("age decrypt: %w", err)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read decrypted stream: %w", err)
	}

	var raw map[string]string
	if err := yaml.Unmarshal(plain, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal secrets YAML: %w", err)
	}

	out := make(map[string][]byte, len(raw))
	for k, v := range raw {
		out[k] = []byte(v)
	}
	return out, nil
}

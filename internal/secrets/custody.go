// Package secrets implements the OwnBase secrets model:
//
//   - Secrets are encrypted at rest in the user's repo under secrets/
//     using filippo.io/age (age-encrypt / age-decrypt CLI-compatible format).
//   - The age private key lives only on the Base at /opt/ownbase/age/key.age,
//     never in the repo, never anywhere off the Base.
//   - At apply time the agent decrypts in-memory and injects only the names
//     a given container declared in its secrets: field — no container can
//     enumerate or receive another container's secrets.
//
// See docs/decisions.md ("Secrets: age-direct, not sops") for why age-direct
// was chosen over sops.
//
// V1 seam: the KeyCustody interface is today backed by a file on the Base;
// a future custody backend (e.g. a hardware key) can satisfy the same
// interface without touching callers.
package secrets

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
)

// DefaultKeyPath is the canonical location of the age private key on the Base.
// Owned by the OwnBase daemon process; permissions must be 0600.
const DefaultKeyPath = "/opt/ownbase/age/key.age"

// KeyCustody is the source of the age decryption identity.
//
// V1 implementation: FileKeyCustody (key file on the Base).
// Post-V1 seam: an Authority-device-backed implementation satisfies the same
// interface without any change to the callers in this package or in M3.
type KeyCustody interface {
	// LoadIdentity returns the age X25519 identity used to decrypt secrets.
	// Errors propagate to the caller as a refusal to issue secrets.
	LoadIdentity() (*age.X25519Identity, error)
}

// FileKeyCustody is the V1 KeyCustody implementation. It reads the age
// private key from a file. The key file must be mode 0600; any other
// permission is refused to prevent accidental group/world exposure.
type FileKeyCustody struct {
	// Path is the key file path. If empty, DefaultKeyPath is used.
	Path string
}

func (f FileKeyCustody) effectivePath() string {
	if f.Path != "" {
		return f.Path
	}
	return DefaultKeyPath
}

// LoadIdentity reads and parses the age private key from the file.
// Returns an error if the file does not exist, has unsafe permissions,
// or does not contain a valid age X25519 identity.
func (f FileKeyCustody) LoadIdentity() (*age.X25519Identity, error) {
	p := f.effectivePath()

	info, err := os.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("age key: stat %s: %w", p, err)
	}
	// Reject group or world read/write/execute bits.
	if info.Mode()&0o077 != 0 {
		return nil, fmt.Errorf("age key: %s has unsafe permissions %04o (must be 0600)",
			p, info.Mode()&0o777)
	}

	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("age key: read %s: %w", p, err)
	}
	return parseIdentity(string(data))
}

// GenerateAndSave generates a fresh age X25519 key pair, writes the private
// key to path with mode 0600 (owner-read-write only), and returns the
// identity (which carries the public key via identity.Recipient()).
//
// This is called once by the installer (M5) on first bootstrap.
// The public key is written to the genesis record so secret files can be
// encrypted for this Base without the private key ever leaving the machine.
func GenerateAndSave(path string) (*age.X25519Identity, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("generate age key: %w", err)
	}
	if err := os.WriteFile(path, []byte(id.String()+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write age key to %s: %w", path, err)
	}
	return id, nil
}

// parseIdentity extracts the first X25519 identity from an age identity file
// (the format produced by age-keygen: "AGE-SECRET-KEY-1…" lines).
func parseIdentity(s string) (*age.X25519Identity, error) {
	ids, err := age.ParseIdentities(strings.NewReader(s))
	if err != nil {
		return nil, fmt.Errorf("parse age identity: %w", err)
	}
	for _, id := range ids {
		if x, ok := id.(*age.X25519Identity); ok {
			return x, nil
		}
	}
	return nil, errors.New("age key: no X25519 identity found in key file")
}

// encryptForRecipient is an internal helper used by tests and developer
// tooling to produce age-encrypted secrets files. Production code only
// decrypts; encryption happens offline via the age CLI or this helper.
func encryptForRecipient(recipient *age.X25519Recipient, plaintext []byte) ([]byte, error) {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)

	go func() {
		w, err := age.Encrypt(pw, recipient)
		if err != nil {
			pw.CloseWithError(err)
			errCh <- err
			return
		}
		if _, err := w.Write(plaintext); err != nil {
			pw.CloseWithError(err)
			errCh <- err
			return
		}
		if err := w.Close(); err != nil {
			pw.CloseWithError(err)
			errCh <- err
			return
		}
		pw.Close()
		errCh <- nil
	}()

	out, err := io.ReadAll(pr)
	if werr := <-errCh; werr != nil {
		return nil, fmt.Errorf("age encrypt: %w", werr)
	}
	if err != nil {
		return nil, fmt.Errorf("age encrypt read: %w", err)
	}
	return out, nil
}

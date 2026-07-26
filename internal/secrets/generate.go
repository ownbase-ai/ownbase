package secrets

// generate.go produces the secret values OwnBase creates on the operator's
// behalf: random passwords and SSH keypairs. These are the values a service
// needs to exist but that nobody needs to choose, and making the operator
// invent them by hand is both a chore and a source of weak credentials.
//
// Everything here is pure value generation with no knowledge of ownbase.yaml
// or of where a value will be stored — see internal/gensecrets for the
// config-driven orchestration that decides what to generate and where it goes.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"

	"golang.org/x/crypto/ssh"
)

// passwordAlphabet deliberately excludes quotes, backslashes, and shell
// metacharacters. Generated passwords travel through YAML, environment
// variables, and connection URLs, and a value that needs escaping somewhere
// along that path turns into an authentication failure nobody can explain.
const passwordAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// GeneratePassword returns a cryptographically random password of n
// characters drawn from passwordAlphabet.
func GeneratePassword(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("generate password: length must be positive, got %d", n)
	}
	out := make([]byte, n)
	max := big.NewInt(int64(len(passwordAlphabet)))
	for i := range out {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		out[i] = passwordAlphabet[idx.Int64()]
	}
	return string(out), nil
}

// SSHKeyPair is a generated SSH keypair in the forms the two ends need:
// PublicAuthorizedKey goes into an authorized_keys file, and PrivatePEM is an
// OpenSSH-format private key.
type SSHKeyPair struct {
	// PublicAuthorizedKey is the single-line authorized_keys form, e.g.
	// "ssh-ed25519 AAAAC3Nza... ownbase-generated".
	PublicAuthorizedKey string

	// PrivatePEM is the OpenSSH PEM-encoded private key, newline-terminated.
	PrivatePEM string
}

// PrivateBase64 returns the private key base64-encoded on a single line.
//
// A PEM private key cannot be passed through an environment variable intact —
// its newlines do not survive the trip — so images that read a key from the
// environment generally expect this form and decode it at startup.
func (k SSHKeyPair) PrivateBase64() string {
	return base64.StdEncoding.EncodeToString([]byte(k.PrivatePEM))
}

// GenerateSSHEd25519 creates a new ed25519 SSH keypair. The comment is
// appended to the public key so an operator reading an authorized_keys file
// can tell where the key came from.
func GenerateSSHEd25519(comment string) (SSHKeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return SSHKeyPair{}, fmt.Errorf("generate ed25519 key: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return SSHKeyPair{}, fmt.Errorf("encode ssh public key: %w", err)
	}

	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return SSHKeyPair{}, fmt.Errorf("marshal ssh private key: %w", err)
	}

	authorized := string(ssh.MarshalAuthorizedKey(sshPub))
	// MarshalAuthorizedKey already ends in a newline; put the comment before it.
	if comment != "" {
		authorized = fmt.Sprintf("%s %s\n", authorized[:len(authorized)-1], comment)
	}

	return SSHKeyPair{
		PublicAuthorizedKey: authorized,
		PrivatePEM:          string(pem.EncodeToMemory(pemBlock)),
	}, nil
}

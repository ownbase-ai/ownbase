package vault

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// NewKeyPair generates an ed25519 owner keypair and returns the private half
// as an OpenSSH PEM string and the public half as an authorized_keys line.
//
// Nothing is written to disk: the PEM goes straight into the vault, and the
// only other place it ever exists is the unlocked agent's memory. comment is
// embedded in both halves so the key is identifiable in a provider's key list
// and in the Base's authorized_keys.
func NewKeyPair(comment string) (privatePEM, publicLine string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate ed25519 key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", "", fmt.Errorf("encode private key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", fmt.Errorf("encode public key: %w", err)
	}
	return string(pem.EncodeToMemory(block)), AuthorizedKeyLine(sshPub, comment), nil
}

// AuthorizedKeyLine renders a public key in authorized_keys form with an
// optional trailing comment and no trailing newline.
func AuthorizedKeyLine(pub ssh.PublicKey, comment string) string {
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if comment != "" {
		line += " " + comment
	}
	return line
}

// SameAuthorizedKey reports whether two authorized_keys lines name the same
// key, ignoring the trailing comment. NewKeyPair embeds "ownbase_<name>";
// readPrivateKeyFile (importing a key the user already had) embeds none — so a
// caller re-importing the exact key material a Base already has must not see
// that as a conflict just because the two lines are spelled differently.
// Falls back to a literal comparison for a line that fails to parse, so an
// unparseable line is still handled the way it always was rather than
// silently treated as equal to everything.
func SameAuthorizedKey(a, b string) bool {
	aKey, _, _, _, aErr := ssh.ParseAuthorizedKey([]byte(a))
	bKey, _, _, _, bErr := ssh.ParseAuthorizedKey([]byte(b))
	if aErr != nil || bErr != nil {
		return a == b
	}
	return string(aKey.Marshal()) == string(bKey.Marshal())
}

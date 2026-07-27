package main

// keygen.go implements `ownbasectl keygen <name>` — the first step of setting
// up a remote Base, and the one that used to be missing.
//
// A cloud provider authorizes an SSH key at server-creation time: you paste a
// public key into the console and the machine boots with it in root's
// authorized_keys. That means the keypair has to exist BEFORE the server does,
// so this cannot be folded into `create`.
//
// Two distinct SSH identities exist in OwnBase and must never be confused:
//
//   - The OWNER key (this command): your machine -> the Base. ownbasectl
//     authenticates with it for every tunnel, install, and git push.
//   - The DEPLOY key (`ownbasectl ssh-key`): the Base -> GitHub. The daemon
//     uses it to clone the config repo and service repos read-only. It is
//     generated on the Base and its private half never leaves it.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
)

func newKeygenCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "keygen <name>",
		Short: "Create the SSH keypair you use to reach a Base, and print the public half",
		Long: `keygen creates an ed25519 keypair at ~/.ssh/ownbase_<name> and prints the
public key to paste into your server provider's "SSH key" field when you
create the machine. Run it before 'ownbasectl create --remote'.

Re-running is safe: an existing key is printed, never regenerated.

Each Base gets its own key, so retiring one Base revokes exactly one
credential. 'create' finds this key automatically — you do not need to pass
--ssh-key.

This is NOT the same as 'ownbasectl ssh-key', which provisions the key the
Base uses to clone your git repos read-only.`,
		Example: `  ownbasectl keygen mybase
  ownbasectl keygen mybase --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKeygen(args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

func runKeygen(name string, jsonOut bool) error {
	keyPath, err := ownerKeyPath(name)
	if err != nil {
		return err
	}

	created := false
	if !fileExists(keyPath) {
		// Comment matches the filename so the key is identifiable in a
		// provider's key list and in authorized_keys, and so it reads the
		// same whether it came from the .pub file or was derived from the
		// private key (see ownerPublicKey).
		if err := generateOwnerKey(keyPath, filepath.Base(keyPath)); err != nil {
			return err
		}
		created = true
	}

	pubKey := ownerPublicKey(keyPath)
	if pubKey == "" {
		return fmt.Errorf("read the public key for %s — the file may be corrupt; delete it and re-run to generate a fresh keypair", keyPath)
	}

	if jsonOut {
		return printJSON(map[string]any{
			"public_key":       pubKey,
			"private_key_path": keyPath,
			"created":          created,
		})
	}

	if created {
		fmt.Printf("Created a new SSH keypair for Base %q at %s\n", name, keyPath)
	} else {
		fmt.Printf("Using the existing SSH keypair for Base %q at %s\n", name, keyPath)
	}
	fmt.Println()
	fmt.Println("Paste this public key into your provider's \"SSH key\" field when you")
	fmt.Println("create the server, so the machine boots with your access already set up:")
	fmt.Println()
	fmt.Println("  " + pubKey)
	fmt.Println()
	fmt.Println("Then, with the server's IP address:")
	fmt.Printf("  ownbasectl create %s --remote root@<ip> --wait\n", name)
	fmt.Println()
	return nil
}

// generateOwnerKey writes a new ed25519 keypair to keyPath (0600) and
// keyPath+".pub" (0644), creating ~/.ssh at 0700 if needed.
func generateOwnerKey(keyPath, comment string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate ed25519 key: %w", err)
	}

	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return fmt.Errorf("encode private key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return fmt.Errorf("encode public key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(keyPath), err)
	}
	// O_EXCL: never clobber an existing private key, even if the caller's
	// existence check raced with another process. Losing an owner key can
	// lock you out of a Base.
	f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", keyPath, err)
	}
	if _, err := f.Write(pem.EncodeToMemory(block)); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", keyPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", keyPath, err)
	}

	pubLine := authorizedKeyLine(sshPub, comment)
	if err := os.WriteFile(keyPath+".pub", []byte(pubLine+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s.pub: %w", keyPath, err)
	}
	return nil
}

// authorizedKeyLine renders a public key in authorized_keys form with a
// trailing comment and no trailing newline.
func authorizedKeyLine(pub ssh.PublicKey, comment string) string {
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if comment != "" {
		line += " " + comment
	}
	return line
}

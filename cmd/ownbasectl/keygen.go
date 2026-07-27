package main

// keygen.go implements `ownbasectl keygen <name>` — the first step of setting
// up a Base.
//
// A cloud provider authorizes an SSH key at server-creation time: you paste a
// public key into the console and the machine boots with it in root's
// authorized_keys. That means the keypair has to exist BEFORE the server does,
// so this cannot be folded into `create`.
//
// The private half goes into the vault and nowhere else — not ~/.ssh, not a
// temp file. It reaches an SSH handshake as a signature from the credential
// agent, so no ownbasectl process, and no agent driving one, ever holds it.
//
// Two distinct SSH identities exist in OwnBase and must never be confused:
//
//   - The OWNER key (this command): your machine -> the Base. ownbasectl
//     authenticates with it for every tunnel, install, and shell.
//   - The DEPLOY key (`ownbasectl ssh-key`): the Base -> GitHub. The daemon
//     uses it to clone the config repo and service repos read-only. It is
//     generated on the Base and its private half never leaves it.

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ownbase/ownbase/internal/vault"
)

func newKeygenCmd() *cobra.Command {
	var (
		jsonOut    bool
		importPath string
	)
	cmd := &cobra.Command{
		Use:   "keygen <name>",
		Short: "Create the SSH keypair you use to reach a Base, and print the public half",
		Long: `keygen creates an ed25519 keypair inside your vault and prints the public
key to paste into your server provider's "SSH key" field when you create the
machine. Run it before 'ownbasectl create --remote'.

Re-running is safe: an existing key is printed, never regenerated.

Each Base gets its own key, so retiring one Base revokes exactly one
credential. 'create' finds the key automatically.

Use --import to adopt a key you already have — one a provider authorized
before you started using OwnBase. The file is copied into the vault and left
where it is; from then on ownbasectl signs with the vault's copy.

This is NOT the same as 'ownbasectl ssh-key', which provisions the key the
Base uses to clone your git repos read-only.`,
		Example: `  ownbasectl keygen mybase
  ownbasectl keygen mybase --json
  ownbasectl keygen mybase --import ~/.ssh/id_ed25519`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKeygen(args[0], importPath, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	cmd.Flags().StringVar(&importPath, "import", "", "import an existing private key file into the vault instead of generating one")
	return cmd
}

func runKeygen(name, importPath string, jsonOut bool) error {
	profile, err := loadProfile(name)
	if err != nil && !isMissingBase(err) {
		return err
	}

	created := false
	switch {
	case importPath != "":
		priv, pub, ierr := readPrivateKeyFile(importPath)
		if ierr != nil {
			return ierr
		}
		if profile.PublicKeyLine() != "" && profile.PublicKeyLine() != pub {
			return withExitCode(exitConflict, fmt.Errorf(
				"Base %q already has a different owner key in the vault; importing would lock you out of the machine that authorized the old one.\n"+
					"       Remove the Base first with 'ownbasectl delete %s --keep-vm' if you really mean to replace its key", name, name))
		}
		profile.PrivateKey, profile.PublicKey = priv, pub
		created = true
	case profile.PublicKeyLine() == "":
		// Comment matches the historical filename so the key is
		// identifiable in a provider's key list and in authorized_keys.
		priv, pub, gerr := vault.NewKeyPair("ownbase_" + name)
		if gerr != nil {
			return gerr
		}
		profile.PrivateKey, profile.PublicKey = priv, pub
		created = true
	}

	if created {
		if err := putProfile(name, profile); err != nil {
			return err
		}
	}

	pubKey := profile.PublicKeyLine()
	if pubKey == "" {
		return fmt.Errorf("no owner key for Base %q after keygen — this should not happen; check 'ownbasectl vault status'", name)
	}

	if jsonOut {
		return printJSON(map[string]any{
			"base":       name,
			"public_key": pubKey,
			"created":    created,
			"stored_in":  "vault",
		})
	}

	switch {
	case created && importPath != "":
		fmt.Printf("Imported %s into the vault as the owner key for Base %q.\n", importPath, name)
	case created:
		fmt.Printf("Created a new SSH keypair for Base %q in your vault.\n", name)
	default:
		fmt.Printf("Using the existing SSH keypair for Base %q from your vault.\n", name)
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

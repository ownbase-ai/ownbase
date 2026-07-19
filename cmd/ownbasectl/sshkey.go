package main

// sshkey.go implements `ownbasectl ssh-key add|list <base>` — provisioning the
// Base's read-only git deploy identity. The daemon generates and stores the
// key under /opt/ownbase/ssh (root, 0700); this command prints the public key
// to register as a read-only deploy key on the config repo and each service
// repo.

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func newSSHKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh-key",
		Short: "Provision and view the Base's read-only git deploy key (add|list)",
	}
	cmd.AddCommand(newSSHKeyAddCmd(), newSSHKeyListCmd())
	return cmd
}

func newSSHKeyAddCmd() *cobra.Command {
	var host string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add <base>",
		Short: "Generate the Base's git deploy key (if needed) and print the public key",
		Long: `add ensures the Base has an ed25519 deploy identity under
/opt/ownbase/ssh, records the given --host in the managed known_hosts, and
prints the public key. Register it as a read-only deploy key on the config
repo and each service repo the Base must clone.`,
		Example: `  ownbasectl ssh-key add mybase --host github.com`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSSHKeyAdd(args[0], host, jsonOut)
		},
	}
	cmd.Flags().StringVar(&host, "host", "github.com", "host to record in the Base's known_hosts")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

func runSSHKeyAdd(base, host string, jsonOut bool) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	payload, _ := json.Marshal(map[string]string{"host": host})
	body, err := apiCall(conn, http.MethodPost, "/ssh-key", payload)
	if err != nil {
		return fmt.Errorf("provision ssh key: %w", err)
	}
	var r struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	if jsonOut {
		return printJSON(map[string]string{"public_key": r.PublicKey})
	}
	fmt.Println("Register this public key as a READ-ONLY deploy key on your config repo")
	fmt.Println("and every service repo this Base must clone:")
	fmt.Println()
	fmt.Println("  " + r.PublicKey)
	fmt.Println()
	return nil
}

func newSSHKeyListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list <base>",
		Short: "Print the Base's current git deploy public key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSSHKeyList(args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

func runSSHKeyList(base string, jsonOut bool) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	body, err := apiGet(conn, "/ssh-key")
	if err != nil {
		return err
	}
	var r struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	if jsonOut {
		return printJSON(map[string]string{"public_key": r.PublicKey})
	}
	if r.PublicKey == "" {
		fmt.Printf("No deploy key yet — run 'ownbasectl ssh-key add %s'.\n", base)
		return nil
	}
	fmt.Println(r.PublicKey)
	return nil
}

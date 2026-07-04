package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newForgejoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forgejo <name>",
		Short: "Print the Forgejo admin username and password",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runForgejoLogin(args[0])
		},
	}
}

// runForgejoLogin retrieves and prints the Forgejo admin credentials from the
// Base via the API, over the SSH tunnel.
func runForgejoLogin(base string) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	body, err := apiGet(conn, "/credentials/forgejo")
	if err != nil {
		return err
	}

	var resp struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	fmt.Println("Forgejo credentials:")
	fmt.Printf("  Username : %s\n", resp.Username)
	fmt.Printf("  Password : %s\n", resp.Password)
	return nil
}

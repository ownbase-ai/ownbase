package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "View and manage per-service secrets (list, get, set, delete)",
		Long: `Secrets are age-encrypted on the Base and injected into a service's
container as environment variables at start. Plaintext travels only
inside the SSH tunnel and is never written to disk on your machine.`,
	}
	cmd.AddCommand(
		newSecretsListCmd(),
		newSecretsGetCmd(),
		newSecretsSetCmd(),
		newSecretsDeleteCmd(),
	)
	return cmd
}

func newSecretsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <name> [service]",
		Short: "List services with secrets, or the key names for one service",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			service := ""
			if len(args) == 2 {
				service = args[1]
			}
			return runSecretsList(args[0], service)
		},
	}
}

// runSecretsList prints either all services that have secrets (no arg) or the
// key names for a specific service.
func runSecretsList(base, service string) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	if service == "" {
		// List all services that have secrets.
		body, err := apiGet(conn, "/secrets")
		if err != nil {
			return err
		}
		var resp struct {
			Services []string `json:"services"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return fmt.Errorf("parse response: %w", err)
		}
		if len(resp.Services) == 0 {
			fmt.Println("No secrets configured on this Base.")
			return nil
		}
		for _, s := range resp.Services {
			fmt.Println(s)
		}
		return nil
	}

	// List keys for a specific service.
	body, err := apiGet(conn, "/secrets/"+service)
	if err != nil {
		return err
	}
	var resp struct {
		Service string   `json:"service"`
		Keys    []string `json:"keys"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	if len(resp.Keys) == 0 {
		fmt.Printf("No secrets configured for service %q.\n", service)
		return nil
	}
	for _, k := range resp.Keys {
		fmt.Println(k)
	}
	return nil
}

func newSecretsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name> <service> <key>",
		Short: "Print the decrypted value of one secret",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSecretsGet(args[0], args[1], args[2])
		},
	}
}

// runSecretsGet prints the decrypted value for a single secret key.
func runSecretsGet(base, service, key string) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	body, err := apiGet(conn, "/secrets/"+service+"/"+key)
	if err != nil {
		return err
	}

	var resp struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	fmt.Print(resp.Value)
	// Trailing newline only on a terminal — piped output (e.g.
	// `secrets get svc KEY | pbcopy`) gets the exact value, nothing more.
	if term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Println()
	}
	return nil
}

func newSecretsSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <name> <service> KEY=VALUE...",
		Short: "Set one or more secrets for a service",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSecretsSet(args[0], args[1], args[2:])
		},
	}
}

// runSecretsSet sets one or more secret key=value pairs for a service.
func runSecretsSet(base, service string, kvArgs []string) error {
	updates := make(map[string]string, len(kvArgs))
	for _, kv := range kvArgs {
		idx := strings.IndexByte(kv, '=')
		if idx < 1 {
			return fmt.Errorf("invalid KEY=VALUE argument: %q (must contain '=')", kv)
		}
		updates[kv[:idx]] = kv[idx+1:]
	}

	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	payload, _ := json.Marshal(updates)
	body, err := apiCall(conn, http.MethodPost, "/secrets/"+service, payload)
	if err != nil {
		return err
	}

	var resp struct {
		Service string `json:"service"`
		Updated int    `json:"updated"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	fmt.Printf("Updated %d secret(s) for service %q.\n", resp.Updated, resp.Service)
	return nil
}

func newSecretsDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name> <service> <key>",
		Short: "Remove one secret from a service",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSecretsDelete(args[0], args[1], args[2])
		},
	}
}

// runSecretsDelete removes a single secret key from a service.
func runSecretsDelete(base, service, key string) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	body, err := apiCall(conn, http.MethodDelete, "/secrets/"+service+"/"+key, nil)
	if err != nil {
		return err
	}

	var resp struct {
		Service string `json:"service"`
		Deleted string `json:"deleted"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	fmt.Printf("Deleted secret %q from service %q.\n", resp.Deleted, resp.Service)
	return nil
}

// apiGet performs an authenticated GET request and returns the response body.
func apiGet(conn *connection, path string) ([]byte, error) {
	return apiCall(conn, http.MethodGet, path, nil)
}

// apiCall performs an authenticated HTTP request and returns the response
// body, using the default 30-second timeout suitable for quick API calls.
func apiCall(conn *connection, method, path string, body []byte) ([]byte, error) {
	return apiCallWithTimeout(conn, method, path, body, 30*time.Second)
}

// apiCallWithTimeout is like apiCall but lets the caller extend the client
// timeout past the default 30 seconds — needed for endpoints that can run
// long (e.g. /backup/run, which the daemon allows up to ten minutes for a
// restic snapshot).
func apiCallWithTimeout(conn *connection, method, path string, body []byte, timeout time.Duration) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, conn.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+conn.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("unauthorized — check that your token is correct\n  Run: ownbasectl adopt <name> --host <host> --token <new-token>")
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(respBody)))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}

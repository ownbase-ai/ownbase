package main

// selfupdate.go implements `ownbasectl self-update <name>` — replace the
// ownbased binary on a Base with a newer signed release and let systemd
// restart it. This is how Caddy image pins move forward: core.Current is
// compiled into the daemon, so a newer OwnBase is what brings a newer Caddy.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newSelfUpdateCmd() *cobra.Command {
	var version string
	cmd := &cobra.Command{
		Use:   "self-update <name>",
		Short: "Replace the OwnBase daemon on a Base with a newer signed release",
		Long: `Download a signed ownbased binary from the OwnBase release server,
verify its minisign signature, install it over the running binary, and let
systemd Restart=always boot the new process.

The core package pin (Caddy image + digest) is compiled into the daemon, so
this is also how a Base picks up a Caddy release that fixes image CVEs.`,
		Example: `  ownbasectl self-update mybase
  ownbasectl self-update mybase --version v0.4.1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSelfUpdate(args[0], version)
		},
	}
	cmd.Flags().StringVar(&version, "version", "latest", "release tag to install (default: latest)")
	return cmd
}

func runSelfUpdate(base, version string) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()
	url := conn.baseURL + "/self-update"

	payload, _ := json.Marshal(map[string]string{"version": version})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if conn.token != "" {
		req.Header.Set("Authorization", "Bearer "+conn.token)
	}

	fmt.Printf("Updating OwnBase daemon on %s to %s...\n", base, version)
	fmt.Println()

	client := &http.Client{Timeout: 15 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		// The tunnel can die when the daemon exits to restart. If we already
		// got a body started we handle it below; a total failure surfaces here.
		return fmt.Errorf("self-update API at %s: %w\n  Is the agent running?", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("unauthorized — the cached token may be stale; remove the profile and run 'ownbasectl adopt' again")
	}
	if resp.StatusCode == http.StatusNotImplemented {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("self-update returned %d: %s", resp.StatusCode, body)
	}

	var gotOK bool
	var sawRestart, sawNoop bool
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "---OK---":
			gotOK = true
			continue
		case strings.Contains(line, "Already running") || strings.Contains(line, "already current"):
			sawNoop = true
		case strings.Contains(line, "Installing to") || strings.Contains(line, "RestartPending") || strings.Contains(line, "will exit"):
			sawRestart = true
		}
		fmt.Println(line)
	}
	// Dropped connection after ---OK--- is expected when the daemon exits
	// to restart. A no-op update also ends with ---OK--- but stays up.
	if gotOK {
		if sawNoop && !sawRestart {
			fmt.Println("\n  Already on the requested version — no restart.")
			return nil
		}
		fmt.Println("\n  Daemon updated. It is restarting under systemd — give it a few seconds.")
		return nil
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("self-update failed — see output above")
}

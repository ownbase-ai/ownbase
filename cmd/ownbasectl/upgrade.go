package main

// upgrade.go implements the 'ownbasectl upgrade' subcommand.
//
// ownbasectl upgrade shows the state of the OwnBase core packages (Forgejo
// and Caddy) as reported by the Base's daemon and, when --apply is passed,
// pulls the latest pinned images and restarts the affected containers.
//
// Core package versions are managed by OwnBase — not by ownbase.yaml. This
// subcommand is the only supported way to upgrade Forgejo and Caddy.
// User services are updated by editing ref: in ownbase.yaml and committing
// (see 'ownbasectl updates' for drift).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newUpgradeCmd() *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "upgrade <name>",
		Short: "Check or apply updates to OwnBase core packages (Forgejo, Caddy)",
		Long: `Core packages (Forgejo and Caddy) are managed by OwnBase — not by
ownbase.yaml. Without --apply, this shows the state of the core packages
as reported by the Base's daemon. With --apply, the daemon pulls the
latest pinned images and restarts the core containers.

User services are updated by editing ref: in ownbase.yaml and committing
(see 'ownbasectl updates' for drift).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !apply {
				return runUpgradeCheck(args[0])
			}
			return runUpgradeApply(args[0])
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false,
		"apply the upgrade (pull new images and restart core containers via the Base daemon); default is check-only")
	return cmd
}

// runUpgradeCheck asks the Base's daemon for the state of the core packages
// (GET /core/status) and prints one line per package. This runs on the Base
// over the SSH tunnel — never against the local machine's Podman.
func runUpgradeCheck(base string) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()
	body, err := apiGet(conn, "/core/status")
	if err != nil {
		return err
	}

	var resp struct {
		Packages []struct {
			Name    string `json:"name"`
			Image   string `json:"image"`
			Digest  string `json:"digest"`
			Running bool   `json:"running"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	fmt.Println("OwnBase core package status")
	fmt.Println("-----------------------------")
	for _, pkg := range resp.Packages {
		status := "stopped"
		if pkg.Running {
			status = "running"
		}
		imageRef := pkg.Image
		hint := ""
		if pkg.Digest != "" {
			imageRef = pkg.Image + "@sha256:" + truncate(strings.TrimPrefix(pkg.Digest, "sha256:"), 12)
		} else {
			hint = fmt.Sprintf("  (no digest pinned — run ownbasectl upgrade %s --apply to pin)", base)
		}
		fmt.Printf("%-10s  %-8s  %s%s\n", pkg.Name, status, imageRef, hint)
	}
	fmt.Printf("\nRun 'ownbasectl upgrade %s --apply' to pull new images and restart core containers.\n", base)
	fmt.Println("After upgrading, image CVEs refresh automatically (~5 min).")
	return nil
}

// runUpgradeApply sends POST /upgrade to the daemon, which pulls the latest
// pinned images and restarts the core containers on the Base, streaming
// progress back.
func runUpgradeApply(base string) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()
	upgradeURL := conn.baseURL + "/upgrade"

	req, err := http.NewRequest(http.MethodPost, upgradeURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if conn.token != "" {
		req.Header.Set("Authorization", "Bearer "+conn.token)
	}

	fmt.Println("About to upgrade the OwnBase core packages (Forgejo, Caddy) on the Base:")
	fmt.Println("  the daemon pulls the latest pinned images and restarts the core")
	fmt.Println("  containers — Forgejo and the reverse proxy restart briefly.")
	fmt.Println()

	client := &http.Client{Timeout: 15 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("upgrade API at %s: %w\n  Is the agent running?", upgradeURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("unauthorized — the cached token may be stale; remove the profile and run 'ownbasectl adopt' again")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upgrade returned %d: %s", resp.StatusCode, body)
	}

	fmt.Println("OwnBase core package upgrade")
	fmt.Println(strings.Repeat("─", 54))

	// Read the streamed response. The daemon ends with "---OK---" on success or
	// returns early (no sentinel) on failure. Check for the sentinel so that
	// callers and automation can detect a failed upgrade even though the HTTP
	// status was already committed as 200.
	var gotOK bool
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "---OK---" {
			gotOK = true
			continue
		}
		fmt.Println(line)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !gotOK {
		return fmt.Errorf("upgrade failed — see output above")
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

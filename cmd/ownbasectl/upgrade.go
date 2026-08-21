package main

// upgrade.go implements the 'ownbasectl upgrade' subcommand.
//
// ownbasectl upgrade shows the state of the OwnBase core package (Caddy)
// as reported by the Base's daemon and, when --apply is passed,
// pulls the latest pinned image and restarts the container.
//
// Core package versions are managed by OwnBase — not by ownbase.yaml. This
// subcommand is the only supported way to upgrade Caddy.
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
	var apply, jsonOut bool
	cmd := &cobra.Command{
		Use:   "upgrade <name>",
		Short: "Check or apply updates to the OwnBase core package (Caddy)",
		Long: `The core package (Caddy) is managed by OwnBase — not by
ownbase.yaml. Without --apply, this shows the state of the core package
as reported by the Base's daemon. With --apply, the daemon pulls the
latest pinned image and restarts the core container.

User services are updated by editing ref: in ownbase.yaml and committing
(see 'ownbasectl updates' for drift).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !apply {
				return runUpgradeCheck(args[0], jsonOut)
			}
			return runUpgradeApply(args[0])
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false,
		"apply the upgrade (pull new images and restart core containers via the Base daemon); default is check-only")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print check-mode status as JSON (ignored with --apply)")
	return cmd
}

// corePackage is one entry from GET /core/status — the JSON shape ownbasectl
// upgrade --json emits so the desktop app never parses human check output.
type corePackage struct {
	Name        string `json:"name"`
	Image       string `json:"image"`
	Digest      string `json:"digest"`
	Running     bool   `json:"running"`
	Recipe      string `json:"recipe,omitempty"`
	ImageRecipe string `json:"image_recipe,omitempty"`
	GoImage     string `json:"go_image,omitempty"`
}

// runUpgradeCheck asks the Base's daemon for the state of the core packages
// (GET /core/status) and prints one line per package. This runs on the Base
// over the SSH tunnel — never against the local machine's Podman.
func runUpgradeCheck(base string, jsonOut bool) error {
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
		Packages []corePackage `json:"packages"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	if resp.Packages == nil {
		resp.Packages = []corePackage{}
	}

	if jsonOut {
		return printJSON(map[string]any{
			"status":   "ok",
			"packages": resp.Packages,
		})
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
		if pkg.GoImage != "" {
			fmt.Printf("            go:     %s\n", pkg.GoImage)
		}
		if pkg.Recipe != "" {
			recipeLine := pkg.Recipe
			if pkg.ImageRecipe != "" && pkg.ImageRecipe != pkg.Recipe {
				recipeLine = fmt.Sprintf("%s (image has %s — rebuild available)", pkg.Recipe, pkg.ImageRecipe)
			} else if pkg.ImageRecipe == pkg.Recipe && pkg.Recipe != "" {
				recipeLine = pkg.Recipe + " (matches image)"
			}
			fmt.Printf("            recipe: %s\n", recipeLine)
		}
	}
	fmt.Printf("\nRun 'ownbasectl upgrade %s --apply' to rebuild the hardened Caddy image and restart it.\n", base)
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

	fmt.Println("About to upgrade the OwnBase core package (Caddy) on the Base:")
	fmt.Println("  the daemon rebuilds the hardened image from its embedded")
	fmt.Println("  Dockerfile (current Go patch + alpine packages) and restarts")
	fmt.Println("  the reverse proxy briefly.")
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

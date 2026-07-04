package main

// updates.go implements the 'ownbasectl updates' subcommand.
//
// ownbasectl updates reads the drift state from the Base's status API and
// prints a per-service table showing the pinned ref, how many commits behind
// each service is from its source repo's default branch, and the newest
// semver tag available.
//
// Updates are driven by the customer: edit ref: in ownbase.yaml and commit.
// The agent fills in blank ref: fields automatically (update.pin_ref).

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newUpdatesCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "updates <name>",
		Short: "Show how far behind each service is from its source repo",
		Long: `Read the drift state from the Base's status API and print a per-service
table: pinned ref, commits behind the default branch, and the newest
semver tag available. Updates are customer-driven — edit ref: in
ownbase.yaml and commit.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := fetchStatusBody(args[0], 10*time.Second)
			if err != nil {
				return err
			}
			if jsonOut {
				section, err := extractStatusSection(body, "updates")
				if err != nil {
					return err
				}
				fmt.Println(string(section))
				return nil
			}
			return printUpdatesSummary(args[0], body)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the updates section of the status payload as JSON")
	return cmd
}

// printUpdatesSummary renders a human-readable drift table from the JSON
// status body.
func printUpdatesSummary(base string, body []byte) error {
	var s map[string]any
	if err := json.Unmarshal(body, &s); err != nil {
		return fmt.Errorf("parse status JSON: %w", err)
	}

	updates, _ := s["updates"].(map[string]any)
	drift, _ := updates["drift"].([]any)

	if len(drift) == 0 {
		fmt.Println("No drift data available yet. The agent reports updates every update-interval tick.")
		return nil
	}

	fmt.Println("─────────────────────────── Service Updates ────────────────────────────")
	fmt.Printf("  %-22s  %-20s  %-8s  %-8s  %s\n",
		"SERVICE", "PINNED REF", "BEHIND", "NEWEST TAG", "STATUS")
	fmt.Println("  " + strings.Repeat("─", 75))

	for _, raw := range drift {
		d, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		service, _ := d["service"].(string)
		ref, _ := d["ref"].(string)
		behind, _ := d["commits_behind"].(float64)
		newestTag, _ := d["newest_tag"].(string)
		upToDate, _ := d["up_to_date"].(bool)

		status := "✓ up to date"
		if !upToDate {
			status = "⚠ behind"
		}

		refDisplay := ref
		if len(refDisplay) > 20 {
			refDisplay = refDisplay[:12] + "..."
		}
		newestDisplay := newestTag
		if newestDisplay == "" {
			newestDisplay = "(no tags)"
		}

		fmt.Printf("  %-22s  %-20s  %-8d  %-8s  %s\n",
			service, refDisplay, int(behind), newestDisplay, status)
	}

	fmt.Println("────────────────────────────────────────────────────────────────────────")
	fmt.Println("  To update a service: edit ref: in ownbase.yaml and commit.")
	fmt.Println("  To get the latest:   delete ref: — the agent will auto-pin.")
	return nil
}

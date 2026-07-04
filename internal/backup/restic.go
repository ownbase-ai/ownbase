package backup

// restic.go wraps the restic binary. Each function maps to one restic
// command. The binary must be installed on the host (apt-get install restic
// or restic's own install script for the latest version).
//
// All commands forward the repository URL and password via environment
// variables (RESTIC_REPOSITORY, RESTIC_PASSWORD_FILE / RESTIC_PASSWORD) to
// avoid exposing them in the process list.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// resticEnv returns the environment variables needed for restic commands.
// Credentials (AWS keys, RESTIC_PASSWORD, etc.) from cfg.Credentials are
// appended last so they take precedence over any ambient values from
// os.Environ(). This allows the rebuild path to supply credentials via the
// process environment while the steady-state path injects them explicitly
// from the age-decrypted backup secret.
func resticEnv(cfg Config) []string {
	env := append(os.Environ(),
		"RESTIC_REPOSITORY="+cfg.Repository,
	)
	if cfg.PasswordFile != "" {
		env = append(env, "RESTIC_PASSWORD_FILE="+cfg.PasswordFile)
	}
	for k, v := range cfg.Credentials {
		env = append(env, k+"="+v)
	}
	return env
}

// resticRun executes a restic command and returns stdout/stderr combined.
func resticRun(ctx context.Context, cfg Config, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "restic", args...)
	cmd.Env = resticEnv(cfg)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ensureRepoInit initialises the restic repository if it does not exist yet.
// Idempotent: "cat" on an existing repo returns "Is there a repository at the
// following location?" and exits non-zero; we detect that and init instead.
func ensureRepoInit(ctx context.Context, cfg Config) error {
	if cfg.DryRun {
		return nil
	}
	// Check if already initialized.
	out, err := resticRun(ctx, cfg, "cat", "config")
	if err == nil {
		return nil // already initialised
	}
	// Restic exits non-zero when the repo is not initialised.
	// A non-existent or uninitialised repo produces a recognisable message.
	if strings.Contains(out, "Is there a repository at") ||
		strings.Contains(out, "repository does not exist") ||
		strings.Contains(out, "no such file or directory") {
		_, initErr := resticRun(ctx, cfg, "init")
		if initErr != nil {
			return fmt.Errorf("restic init: %w", initErr)
		}
		return nil
	}
	// Some other error (e.g. wrong password, network issue).
	return fmt.Errorf("restic cat config: %w\noutput: %s", err, out)
}

// takeSnapshot runs `restic backup` and returns the new snapshot ID.
func takeSnapshot(ctx context.Context, cfg Config) (string, error) {
	if cfg.DryRun {
		return "dry-run-snapshot", nil
	}

	args := []string{"backup", "--json"}
	args = append(args, cfg.Paths...)

	out, err := resticRun(ctx, cfg, args...)
	if err != nil {
		return "", fmt.Errorf("restic backup: %w\noutput: %s", err, out)
	}

	// Parse the JSON summary to extract the snapshot ID.
	// Restic --json emits multiple JSON objects; the last one is the summary.
	id := parseSnapshotID(out)
	return id, nil
}

// snapshotSummary is the JSON structure restic emits at the end of `backup --json`.
type snapshotSummary struct {
	MessageType string `json:"message_type"` // "summary"
	SnapshotID  string `json:"snapshot_id"`
}

// parseSnapshotID extracts the snapshot ID from restic --json backup output.
// Returns empty string if it cannot be found.
func parseSnapshotID(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var s snapshotSummary
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			continue
		}
		if s.MessageType == "summary" && s.SnapshotID != "" {
			return s.SnapshotID
		}
	}
	return ""
}

// pruneOld runs `restic forget --prune` using the configured retention policy.
func pruneOld(ctx context.Context, cfg Config) error {
	if cfg.DryRun {
		return nil
	}
	_, err := resticRun(ctx, cfg,
		"forget", "--prune",
		"--keep-within", fmt.Sprintf("%dd", cfg.RetentionDays),
		"--keep-last", "3", // always keep at least 3 snapshots
	)
	if err != nil {
		return fmt.Errorf("restic forget: %w", err)
	}
	return nil
}

// latestSnapshotID returns the ID of the most recent restic snapshot.
func latestSnapshotID(ctx context.Context, cfg Config) (string, error) {
	// Use --latest 1 (replaces deprecated --last in restic ≥ 0.16).
	out, err := resticRun(ctx, cfg, "snapshots", "--json", "--latest", "1")
	if err != nil {
		return "", fmt.Errorf("restic snapshots: %w\n%s", err, out)
	}

	// Strip any non-JSON prefix lines (e.g. deprecation warnings written to
	// stdout by older restic builds).
	jsonLine := extractJSONArray(out)
	var snapshots []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(jsonLine), &snapshots); err != nil {
		return "", fmt.Errorf("parse restic snapshots: %w", err)
	}
	if len(snapshots) == 0 {
		return "", fmt.Errorf("no snapshots found in repository")
	}
	return snapshots[0].ID, nil
}

// extractJSONArray returns the first line that starts with '[' from s.
// restic sometimes emits deprecation warnings before the JSON output.
func extractJSONArray(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			return line
		}
	}
	return strings.TrimSpace(s)
}

// restoreSnapshot restores a snapshot to targetDir.
// The target directory is created by restic if it does not exist.
func restoreSnapshot(ctx context.Context, cfg Config, snapshotID, targetDir string) error {
	if cfg.DryRun {
		return nil
	}
	_, err := resticRun(ctx, cfg,
		"restore", snapshotID,
		"--target", targetDir,
	)
	if err != nil {
		return fmt.Errorf("restic restore %s: %w", snapshotID, err)
	}
	return nil
}

// checkRepo runs `restic check` to verify repository consistency.
// --read-data-subset=5% reads a random 5% sample of pack data to detect
// corruption without the cost of reading everything.
func checkRepo(ctx context.Context, cfg Config) error {
	if cfg.DryRun {
		return nil
	}
	_, err := resticRun(ctx, cfg, "check", "--read-data-subset=5%")
	if err != nil {
		return fmt.Errorf("restic check: %w", err)
	}
	return nil
}

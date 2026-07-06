package update

// pr.go contains helpers for committing field edits to ownbase.yaml on the
// local config repo (used by blank-ref resolution) and YAML-editing utilities.
//
// The PR-generation path (OpenUpdatePR, branch creation, PR body templates)
// has been removed. Updates are now driven by the user editing ref: and
// committing directly — see docs/decisions.md ("Updates and drift").

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// YAML field editing
// ---------------------------------------------------------------------------

// BumpRef replaces the ref: field for the named service in yamlContent.
// Handles both "replacing an existing ref:" and "inserting a new ref: field".
// Exported so tests can verify the YAML edit logic independently.
func BumpRef(yamlContent, serviceName, oldRef, newRef string) (string, error) {
	return bumpField(yamlContent, serviceName, "ref", oldRef, newRef)
}

// BumpDigest replaces the digest: field for the named service in yamlContent.
// Handles both "adding a new digest:" field and "replacing an existing one".
// Exported so tests can verify the YAML edit logic independently.
func BumpDigest(yamlContent, serviceName, oldDigest, newDigest string) (string, error) {
	return bumpField(yamlContent, serviceName, "digest", oldDigest, newDigest)
}

// bumpField replaces (or inserts) fieldName: newValue within the named service
// block in yamlContent. When oldValue is empty the field is inserted; when
// oldValue is non-empty the existing field line is replaced. Returns an error
// when serviceName is not found or when oldValue is non-empty and the field is
// absent (unexpected state).
func bumpField(yamlContent, serviceName, fieldName, oldValue, newValue string) (string, error) {
	lines := strings.Split(yamlContent, "\n")
	inService := false
	indentLevel := 0
	replaced := false
	fieldKey := fieldName + ":"

	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)

		// Detect the start of the target service block.
		if strings.HasPrefix(trimmed, serviceName+":") {
			inService = true
			indentLevel = indent
			continue
		}

		if !inService {
			continue
		}

		// Exit the service block when we reach a sibling key at the same
		// (or lower) indent level — this is how YAML nesting works.
		if indent <= indentLevel && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			// If we need to insert (oldValue=="") and haven't yet, insert
			// the new field just before this sibling.
			if !replaced && oldValue == "" {
				insertLine := strings.Repeat(" ", indentLevel+2) + fieldName + ": " + newValue
				lines = append(lines[:i], append([]string{insertLine}, lines[i:]...)...)
				replaced = true
			}
			inService = false
			continue
		}

		// Replace the existing field line.
		if strings.HasPrefix(strings.TrimSpace(line), fieldKey) {
			lines[i] = strings.Repeat(" ", indent) + fieldName + ": " + newValue
			replaced = true
		}
	}

	// Handle services that are the last block in the file (no trailing sibling).
	if inService && !replaced && oldValue == "" {
		lines = append(lines, strings.Repeat(" ", indentLevel+2)+fieldName+": "+newValue)
		replaced = true
	}

	if !replaced {
		if oldValue == "" {
			return "", fmt.Errorf("service %q not found in ownbase.yaml", serviceName)
		}
		return "", fmt.Errorf("service %q: field %q not found in ownbase.yaml", serviceName, fieldName)
	}
	return strings.Join(lines, "\n"), nil
}

// shortRef returns a display-friendly version of a git ref. Long SHAs are
// truncated to 12 characters; tags and branch names are returned as-is.
func shortRef(ref string) string {
	if ref == "" {
		return "(unpinned)"
	}
	if len(ref) == 40 && isHex(ref) {
		return ref[:12]
	}
	return ref
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Local config-repo commit helper (used by blank-ref resolution)
// ---------------------------------------------------------------------------

// CommitFile writes content to path (relative to cfg.CheckoutPath), then
// commits and pushes it from the checkout to its origin — the local bare
// repo at /opt/ownbase/repo. This is the single front-door for programmatic
// ownbase.yaml edits (blank-ref resolution, the daemon's /backup/configure
// API): always a real commit through the checkout, exactly like a user's
// own `git push`, so the post-receive hook and reconcile loop see it the
// same way regardless of who made the change.
func CommitFile(ctx context.Context, cfg Config, path, content, commitMsg string) error {
	_ = ctx // no network I/O; kept for API symmetry with the update loop
	fullPath := filepath.Join(cfg.CheckoutPath, path)
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", fullPath, err)
	}
	if out, err := exec.Command("git", "-C", cfg.CheckoutPath, "add", path).CombinedOutput(); err != nil {
		return fmt.Errorf("git add %s: %w\n%s", path, err, out)
	}
	if out, err := exec.Command("git", "-C", cfg.CheckoutPath, "commit", "-m", commitMsg).CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", cfg.CheckoutPath, "push", "origin", "HEAD").CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %w\n%s", err, out)
	}
	return nil
}

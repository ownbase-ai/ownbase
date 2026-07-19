package update

// pr.go contains YAML-editing utilities for ownbase.yaml (BumpRef/BumpDigest).
//
// The PR-generation path (OpenUpdatePR, branch creation, PR body templates)
// and the daemon-side commit path (CommitFile) have both been removed. All
// config mutations are now made client-side by ownbasectl, which clones the
// external config repo, edits ownbase.yaml, commits, and pushes with the
// operator's own git credentials — see docs/decisions.md ("Updates and drift").

import (
	"fmt"
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

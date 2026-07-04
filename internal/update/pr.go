package update

// pr.go contains helpers for committing field edits to ownbase.yaml on the
// Forgejo config repo (used by blank-ref resolution) and YAML-editing utilities.
//
// The PR-generation path (OpenUpdatePR, branch creation, PR body templates)
// has been removed. Updates are now driven by the user editing ref: and
// committing directly — see docs/decisions.md ("Updates and drift").

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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
// Forgejo file-commit helpers (used by blank-ref resolution)
// ---------------------------------------------------------------------------

// CommitFile commits content to path in the Forgejo config repo on branch
// main, creating the file if it does not exist or updating it in place if it
// does. Exported so callers outside the update loop (e.g. the daemon's
// /backup/configure API) can persist an ownbase.yaml edit through the same
// front-door commit path used by blank-ref resolution — never a local git
// commit that could race with the Forgejo→bare-repo sync.
func CommitFile(ctx context.Context, cfg Config, path, content, commitMsg string) error {
	return forgejoCommitFile(ctx, cfg, path, content, commitMsg)
}

func forgejoCommitFile(ctx context.Context, cfg Config, path, content, commitMsg string) error {
	sha, _ := forgejoGetFileSHA(ctx, cfg, "main", path)

	payload := map[string]any{
		"message": commitMsg,
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"branch":  "main",
	}

	apiPath := fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s", cfg.ForgejoUser, cfg.RepoName, path)

	if sha != "" {
		payload["sha"] = sha
		bodyBytes, _ := json.Marshal(payload)
		return forgejoPut(ctx, cfg, apiPath, bodyBytes, http.StatusOK)
	}
	bodyBytes, _ := json.Marshal(payload)
	return forgejoPost(ctx, cfg, apiPath, bodyBytes, http.StatusCreated)
}

func forgejoGetFileSHA(ctx context.Context, cfg Config, branch, path string) (string, error) {
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/%s?ref=%s",
		cfg.ForgejoURL, cfg.ForgejoUser, cfg.RepoName, path, branch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "token "+cfg.ForgejoToken)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		SHA string `json:"sha"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return result.SHA, nil
}

func forgejoPost(ctx context.Context, cfg Config, path string, body []byte, allowedStatuses ...int) error {
	return forgejoRequest(ctx, cfg, http.MethodPost, path, body, allowedStatuses...)
}

func forgejoPut(ctx context.Context, cfg Config, path string, body []byte, allowedStatuses ...int) error {
	return forgejoRequest(ctx, cfg, http.MethodPut, path, body, allowedStatuses...)
}

func forgejoRequest(ctx context.Context, cfg Config, method, path string, body []byte, allowedStatuses ...int) error {
	url := cfg.ForgejoURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+cfg.ForgejoToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	for _, s := range allowedStatuses {
		if resp.StatusCode == s {
			return nil
		}
	}
	b, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, b)
}

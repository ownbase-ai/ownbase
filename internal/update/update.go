// Package update implements blank-ref resolution and drift reporting for the
// OwnBase update loop.
//
// The loop (Principle 9):
//
//	customer edits ref: -> commits -> hook -> reconcile -> BUILD from repo@ref -> deploy
//	agent (periodic):  resolve blank refs -> commit -> reconcile -> build
//	agent (periodic):  compute drift -> report via ownbasectl updates
//
// Updates are never silent in-place mutations. Every service change is a
// commit to the customer-owned repo — either by the customer directly, or by
// the agent filling in a blank ref:.
//
// # Blank-ref resolution
//
// When a service has no ref: in ownbase.yaml, ResolveBlankRefs resolves the
// default-branch HEAD commit SHA via the Forgejo API and commits it back.
// The commit triggers the existing hook -> reconcile -> build path. Once set,
// the ref is never rewritten by the agent.
//
// # Drift reporting
//
// ComputeDrift computes, for each service with a concrete ref:, how many
// commits behind that ref is from the default-branch HEAD, and what the
// newest semver tag is. Results are surfaced by ownbasectl updates.
package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/schema"
)

// DefaultCheckInterval is how often the agent polls for blank refs and drift.
const DefaultCheckInterval = 6 * time.Hour

// ServiceDrift holds the drift state for one service.
type ServiceDrift struct {
	// Service is the service key in ownbase.yaml.
	Service string
	// Ref is the currently pinned ref (branch, tag, or SHA).
	Ref string
	// Branch is the default branch of the source repo (e.g. "main").
	Branch string
	// CommitsBehind is how many commits the pinned ref is behind the default
	// branch HEAD. Zero means up to date on the branch dimension.
	CommitsBehind int
	// NewestTag is the highest semver tag available in the Forgejo repo.
	// Empty when the repo has no tags.
	NewestTag string
	// UpToDate is true when CommitsBehind == 0 and (NewestTag == "" or
	// NewestTag == Ref or Ref is already at the newest tag).
	UpToDate bool
}

// Config controls the update detection and blank-ref resolution workflow.
type Config struct {
	// CheckoutPath is the path to the ownbase checkout where ownbase.yaml lives.
	CheckoutPath string

	// ForgejoURL is the base URL of the on-Base Forgejo instance.
	ForgejoURL string

	// ForgejoToken is the Forgejo API token. Required for detection and
	// blank-ref resolution. Empty disables the update loop entirely.
	ForgejoToken string

	// ForgejoUser is the Forgejo user that owns the config repository (and is
	// the default owner for repos without an explicit org/ prefix in source:).
	ForgejoUser string

	// RepoName is the Forgejo repository name that holds ownbase.yaml.
	RepoName string

	// DryRun previews detection without making changes.
	DryRun bool
}

// ServiceRef is the minimal info the update loop needs about one service.
type ServiceRef struct {
	// Source is the Forgejo repo path — either from source: directly or
	// derived from mirror: via mirrorForgejoPath (e.g. "mirrors-postgres").
	Source string
	// Ref is the pinned ref (branch, tag, or SHA). Empty means blank (pending resolution).
	Ref string
}

// ServicesFromConfig extracts service refs from a parsed OwnbaseConfig.
// Both source: and mirror: services are included; mirror: services resolve to
// their derived Forgejo path (mirrors-<basename>) for detection.
func ServicesFromConfig(cfg *schema.OwnbaseConfig) map[string]ServiceRef {
	out := make(map[string]ServiceRef, len(cfg.Services))
	for name, svc := range cfg.Services {
		source := svc.Source
		if source == "" && svc.Mirror != "" {
			source = mirrorForgejoPath(svc.Mirror)
		}
		out[name] = ServiceRef{
			Source: source,
			Ref:    svc.Ref,
		}
	}
	return out
}

// mirrorForgejoPath derives the Forgejo path for a mirror URL.
// Returns "mirrors-<basename>" (with a dash, not a slash) — matching
// compiler.MirrorForgejoPath and githost.MirrorForgejoRepoName.
func mirrorForgejoPath(mirrorURL string) string {
	u := strings.TrimRight(mirrorURL, "/")
	u = strings.TrimSuffix(u, ".git")
	if idx := strings.Index(u, ":"); idx >= 0 && !strings.Contains(u[:idx], "/") {
		u = u[idx+1:]
	}
	idx := strings.LastIndex(u, "/")
	if idx < 0 {
		return "mirrors-" + u
	}
	return "mirrors-" + u[idx+1:]
}

// ResolveBlankRefs checks each service for a missing ref: and, for each that
// has none, resolves the default-branch HEAD commit SHA and commits it back to
// ownbase.yaml via the Forgejo contents API. Each write-back is recorded in
// the audit log as update.pin_ref (autonomous tier).
//
// Non-fatal per service: an error resolving one service is logged and skipped.
func ResolveBlankRefs(ctx context.Context, cfg Config, services map[string]ServiceRef, al authz.AuditLogger) {
	cfgPath := filepath.Join(cfg.CheckoutPath, "ownbase.yaml")

	for name, svc := range services {
		if svc.Source == "" || strings.TrimSpace(svc.Ref) != "" {
			continue
		}

		_, sha, err := resolveDefaultBranchHead(ctx, cfg, svc.Source)
		if err != nil {
			fmt.Fprintf(os.Stderr, "update: resolve blank ref for %s (%s): %v\n", name, svc.Source, err)
			continue
		}
		if sha == "" {
			fmt.Fprintf(os.Stderr, "update: resolve blank ref for %s (%s): no commits yet\n", name, svc.Source)
			continue
		}

		if cfg.DryRun {
			fmt.Fprintf(os.Stderr, "update: [dry-run] would pin %s to %s\n", name, sha)
			continue
		}

		// Read current ownbase.yaml.
		original, err := os.ReadFile(cfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "update: read ownbase.yaml for %s: %v\n", name, err)
			continue
		}

		updated, err := BumpRef(string(original), name, "", sha)
		if err != nil {
			fmt.Fprintf(os.Stderr, "update: bump ref for %s: %v\n", name, err)
			continue
		}

		commitMsg := fmt.Sprintf("chore(pin): auto-pin %s to %s\n\nref: was blank; resolved to default-branch HEAD", name, shortRef(sha))
		if err := forgejoCommitFile(ctx, cfg, "ownbase.yaml", updated, commitMsg); err != nil {
			fmt.Fprintf(os.Stderr, "update: commit blank-ref resolution for %s: %v\n", name, err)
			continue
		}

		if al != nil {
			action, _ := schema.NewAction(schema.ActionUpdatePinRef, name)
			_ = al.Record(action, authz.OutcomeApplied, "")
		}

		fmt.Fprintf(os.Stderr, "update: pinned %s to %s\n", name, shortRef(sha))
	}
}

// ComputeDrift returns a ServiceDrift entry for each service that has a
// concrete ref:. Services with no ref: are skipped (they will be resolved by
// ResolveBlankRefs). Per-service errors are non-fatal.
func ComputeDrift(ctx context.Context, cfg Config, services map[string]ServiceRef) []ServiceDrift {
	var result []ServiceDrift

	for name, svc := range services {
		if svc.Source == "" || strings.TrimSpace(svc.Ref) == "" {
			continue
		}
		if cfg.ForgejoToken == "" {
			continue
		}

		branch, headSHA, err := resolveDefaultBranchHead(ctx, cfg, svc.Source)
		if err != nil {
			fmt.Fprintf(os.Stderr, "update: drift %s (%s): resolve head: %v\n", name, svc.Source, err)
			continue
		}

		behind := 0
		if headSHA != "" && headSHA != svc.Ref {
			behind, err = commitsBehind(ctx, cfg, svc.Source, svc.Ref, headSHA)
			if err != nil {
				fmt.Fprintf(os.Stderr, "update: drift %s (%s): commits behind: %v\n", name, svc.Source, err)
			}
		}

		newestTag, err := fetchLatestSourceRef(ctx, cfg, svc.Source)
		if err != nil {
			fmt.Fprintf(os.Stderr, "update: drift %s (%s): newest tag: %v\n", name, svc.Source, err)
		}

		upToDate := behind == 0 && (newestTag == "" || newestTag == svc.Ref)

		result = append(result, ServiceDrift{
			Service:       name,
			Ref:           svc.Ref,
			Branch:        branch,
			CommitsBehind: behind,
			NewestTag:     newestTag,
			UpToDate:      upToDate,
		})
	}

	return result
}

// MirrorForgejoPathForTest exposes mirrorForgejoPath for Tier-1 regression tests.
func MirrorForgejoPathForTest(mirrorURL string) string {
	return mirrorForgejoPath(mirrorURL)
}

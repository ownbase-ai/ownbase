// Package update implements blank-ref resolution and drift reporting for the
// OwnBase update loop.
//
// The loop (Principle 9):
//
//	user edits ref: -> commits -> hook -> reconcile -> BUILD from repo@ref -> deploy
//	agent (periodic):  resolve blank refs -> commit -> reconcile -> build
//	agent (periodic):  compute drift -> report via ownbasectl updates
//
// Updates are never silent in-place mutations. Every service change is a
// commit to the user-owned repo — either by the user directly, or by
// the agent filling in a blank ref:.
//
// # Blank-ref resolution
//
// When a service has no ref: in ownbase.yaml, ResolveBlankRefs resolves the
// default-branch HEAD commit SHA from the service's local bare repo (see
// internal/repos) and commits it back. The commit triggers the existing
// hook -> reconcile -> build path. Once set, the ref is never rewritten by
// the agent.
//
// # Drift reporting
//
// ComputeDrift computes, for each service with a concrete ref:, how many
// commits behind that ref is from the default-branch HEAD, and what the
// newest semver tag is — all read from the local bare repo. Results are
// surfaced by ownbasectl updates.
package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/repos"
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
	// NewestTag is the highest semver tag available in the local bare repo.
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

	// ReposDir is the root directory containing one bare repo per service
	// (see internal/repos). Empty means repos.DefaultReposDir
	// (/opt/ownbase/repos). Overridable so tests can point detection at a
	// throwaway directory instead of the real production path.
	ReposDir string

	// DryRun previews detection without making changes.
	DryRun bool
}

// repoPath returns the local bare-repo path for a service's source name,
// rooted at cfg.ReposDir (or repos.DefaultReposDir when unset).
func (c Config) repoPath(name string) string {
	root := c.ReposDir
	if root == "" {
		root = repos.DefaultReposDir
	}
	return filepath.Join(root, name)
}

// ServiceRef is the minimal info the update loop needs about one service.
type ServiceRef struct {
	// Source is the local bare-repo name under /opt/ownbase/repos/ — either
	// from source: directly or derived from mirror: via mirrorRepoName
	// (e.g. "mirrors-postgres").
	Source string
	// Ref is the pinned ref (branch, tag, or SHA). Empty means blank (pending resolution).
	Ref string
}

// ServicesFromConfig extracts service refs from a parsed OwnbaseConfig.
// Both source: and mirror: services are included; mirror: services resolve
// to their derived local bare-repo name (mirrors-<basename>) for detection.
func ServicesFromConfig(cfg *schema.OwnbaseConfig) map[string]ServiceRef {
	out := make(map[string]ServiceRef, len(cfg.Services))
	for name, svc := range cfg.Services {
		source := svc.Source
		if source == "" && svc.Mirror != "" {
			source = mirrorRepoName(svc.Mirror)
		}
		out[name] = ServiceRef{
			Source: source,
			Ref:    svc.Ref,
		}
	}
	return out
}

// mirrorRepoName derives the local bare-repo name for a mirror URL.
// Returns "mirrors-<basename>" (with a dash, not a slash) — matching
// compiler.MirrorRepoName.
func mirrorRepoName(mirrorURL string) string {
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
// ownbase.yaml via the local config-repo front door (see CommitFile). Each
// write-back is recorded in the audit log as update.pin_ref (autonomous tier).
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
		if err := CommitFile(ctx, cfg, "ownbase.yaml", updated, commitMsg); err != nil {
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

// MirrorRepoNameForTest exposes mirrorRepoName for Tier-1 regression tests.
func MirrorRepoNameForTest(mirrorURL string) string {
	return mirrorRepoName(mirrorURL)
}

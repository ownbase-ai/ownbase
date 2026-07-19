// Package update implements drift reporting for the OwnBase update loop.
//
// Updates are never silent in-place mutations. A service moves to a new ref
// only when the operator runs `ownbasectl deploy`, which resolves the ref to
// a concrete commit SHA and commits it to the external config repo. There is
// no automatic blank-ref pinning.
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
	// NewestTag == Ref or Ref is already at the newest tag). Always true
	// when Ref is a full commit SHA — see ComputeDrift.
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
	// Source is the local bare-repo name under /opt/ownbase/repos/. Every
	// service's local clone is keyed by its service name (see
	// compiler.build), so Source == the service key.
	Source string
	// Ref is the pinned ref (branch, tag, or SHA). Empty means the build
	// falls back to the repo's default HEAD.
	Ref string
}

// ServicesFromConfig extracts service refs from a parsed OwnbaseConfig. Each
// service's local bare repo is keyed by its service name, so Source is set to
// the service key for drift detection against /opt/ownbase/repos/<name>.
func ServicesFromConfig(cfg *schema.OwnbaseConfig) map[string]ServiceRef {
	out := make(map[string]ServiceRef, len(cfg.Services))
	for name, svc := range cfg.Services {
		out[name] = ServiceRef{
			Source: name,
			Ref:    svc.Ref,
		}
	}
	return out
}

// ComputeDrift returns a ServiceDrift entry for each service that has a
// concrete ref:. Services with no ref: are skipped (a ref is only ever set
// explicitly by `ownbasectl deploy`). Per-service errors are non-fatal.
func ComputeDrift(ctx context.Context, cfg Config, services map[string]ServiceRef) []ServiceDrift {
	var result []ServiceDrift

	for name, svc := range services {
		if svc.Source == "" || strings.TrimSpace(svc.Ref) == "" {
			continue
		}

		// A full commit SHA is already maximally pinned — there is no
		// newer version of "this exact commit" to be behind, and comparing
		// it against the newest semver tag would produce a false positive
		// (a raw SHA essentially never matches a tag name). Skip the
		// tag-based drift comparison and report it as up to date.
		if isCommitSHA(svc.Ref) {
			result = append(result, ServiceDrift{
				Service:  name,
				Ref:      svc.Ref,
				UpToDate: true,
			})
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

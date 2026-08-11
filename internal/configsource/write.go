package configsource

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ownbase/ownbase/internal/schema"
)

// BranchPrefix is required on every daemon-pushed proposal branch.
// Branch protection on the tracked ref (usually main) is the forge-side
// enforcement that these never become the live config without a merge.
const BranchPrefix = "ownbase/agent/"

// PushResult is the outcome of a successful branch-only config push.
type PushResult struct {
	Branch  string `json:"branch"`
	SHA     string `json:"sha"`
	RepoURL string `json:"repo_url"`
	Message string `json:"message"`
}

// PushBranch clones src into a temp workdir, writes content as ownbase.yaml,
// commits, and pushes to refs/heads/<branch> only. It never updates the
// tracked ref (src.EffectiveRef()). The live checkout is not touched.
//
// content must already be schema-valid ownbase.yaml (caller should validate).
// branch, when empty, is generated as ownbase/agent/<utc>.
// message defaults when empty.
// gitEnv should include GIT_SSH_COMMAND for a key that can write the config repo.
func PushBranch(ctx context.Context, src Source, content, message, branch string, gitEnv []string) (PushResult, error) {
	var zero PushResult
	if !src.Configured() {
		return zero, fmt.Errorf("config source not configured")
	}
	if strings.TrimSpace(content) == "" {
		return zero, fmt.Errorf("content is empty")
	}
	// Defense in depth: refuse invalid yaml even if the caller skipped validate.
	if _, err := schema.ParseConfig(strings.NewReader(content)); err != nil {
		return zero, fmt.Errorf("invalid ownbase.yaml: %w", err)
	}

	branch, err := normalizeBranch(branch, src.EffectiveRef())
	if err != nil {
		return zero, err
	}
	if message = strings.TrimSpace(message); message == "" {
		message = "config: proposal from ownbased"
	}

	tmp, err := os.MkdirTemp("", "ownbase-config-write-*")
	if err != nil {
		return zero, fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	workdir := filepath.Join(tmp, "repo")
	if out, err := runGit(ctx, gitEnv, "", "clone", "--depth", "1", "--branch", src.EffectiveRef(), src.RepoURL, workdir); err != nil {
		// Depth-1 + branch can fail for tag/SHA refs; fall back to full clone + checkout.
		_ = os.RemoveAll(workdir)
		if out2, err2 := runGit(ctx, gitEnv, "", "clone", src.RepoURL, workdir); err2 != nil {
			return zero, fmt.Errorf("clone config repo: %w\n%s\n%s", err2, out, out2)
		}
		ref := src.EffectiveRef()
		target := "origin/" + ref
		if !revParseVerifies(ctx, gitEnv, workdir, target) {
			target = ref
		}
		if out3, err3 := runGit(ctx, gitEnv, workdir, "checkout", "-B", "base", target); err3 != nil {
			return zero, fmt.Errorf("checkout %s: %w\n%s", target, err3, out3)
		}
	}

	// New branch from current HEAD (tracked tip).
	if out, err := runGit(ctx, gitEnv, workdir, "checkout", "-B", branch); err != nil {
		return zero, fmt.Errorf("create branch %s: %w\n%s", branch, err, out)
	}

	yamlPath := filepath.Join(workdir, "ownbase.yaml")
	if err := os.WriteFile(yamlPath, []byte(content), 0o644); err != nil {
		return zero, fmt.Errorf("write ownbase.yaml: %w", err)
	}
	if out, err := runGit(ctx, gitEnv, workdir, "add", "ownbase.yaml"); err != nil {
		return zero, fmt.Errorf("git add: %w\n%s", err, out)
	}
	// Empty commit? Refuse.
	if _, err := runGit(ctx, gitEnv, workdir, "diff", "--cached", "--quiet"); err == nil {
		return zero, fmt.Errorf("no change: ownbase.yaml matches the tracked tip")
	}

	commitEnv := append([]string{}, gitEnv...)
	commitEnv = append(commitEnv,
		"GIT_AUTHOR_NAME=ownbased",
		"GIT_AUTHOR_EMAIL=ownbased@localhost",
		"GIT_COMMITTER_NAME=ownbased",
		"GIT_COMMITTER_EMAIL=ownbased@localhost",
	)
	if out, err := runGit(ctx, commitEnv, workdir, "commit", "-m", message); err != nil {
		return zero, fmt.Errorf("git commit: %w\n%s", err, out)
	}

	shaOut, err := runGit(ctx, gitEnv, workdir, "rev-parse", "HEAD")
	if err != nil {
		return zero, fmt.Errorf("rev-parse HEAD: %w\n%s", err, shaOut)
	}
	sha := strings.TrimSpace(string(shaOut))

	// Branch-only push. Never HEAD:main / tracked ref. Force-with-lease so a
	// named ownbase/agent/* proposal can be updated/retried; lease refuses if
	// someone else moved the branch tip since our clone (we always base on
	// tracked tip, so lease is against whatever was there if the branch
	// existed — plain + fails on non-ff).
	refspec := "+HEAD:refs/heads/" + branch
	if out, err := runGit(ctx, gitEnv, workdir, "push", "--force-with-lease", "origin", refspec); err != nil {
		return zero, fmt.Errorf("git push %s: %w\n%s", refspec, err, out)
	}

	return PushResult{
		Branch:  branch,
		SHA:     sha,
		RepoURL: src.RepoURL,
		Message: message,
	}, nil
}

func normalizeBranch(branch, trackedRef string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		branch = BranchPrefix + time.Now().UTC().Format("20060102T150405Z")
	}
	// Disallow path tricks and force-push to tracked ref under another name.
	if strings.Contains(branch, "..") || strings.HasPrefix(branch, "/") || strings.Contains(branch, "\\") {
		return "", fmt.Errorf("invalid branch name %q", branch)
	}
	if strings.ContainsAny(branch, " \t\n\r~^:?*[\\") {
		return "", fmt.Errorf("invalid branch name %q", branch)
	}
	if !strings.HasPrefix(branch, BranchPrefix) {
		return "", fmt.Errorf("branch must start with %q (got %q)", BranchPrefix, branch)
	}
	if branch == trackedRef || branch == "main" || branch == "master" {
		return "", fmt.Errorf("refusing to push to tracked/protected ref %q", branch)
	}
	return branch, nil
}

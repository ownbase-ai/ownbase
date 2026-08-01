package main

// configrepo.go is the single client-side mutation path for a Base's
// ownbase.yaml. In the explicit deploy model the config lives in an external
// git repo (GitHub), the Base only reads it, and every change — deploy,
// config set, service add/update/remove, backup setup — is committed and
// pushed from the operator's machine with the operator's own git credentials,
// then applied by asking the daemon to pull + reconcile.
//
// mutateConfig is the workhorse: clone the config repo to a temp dir, hand the
// current ownbase.yaml text to an edit function, validate the result, commit,
// push, and POST /reconcile.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/vault"
)

// ownbaseYAMLName is the config file committed to the config repo.
const ownbaseYAMLName = "ownbase.yaml"

// gitTimeout bounds every client-side git invocation. Without it an HTTPS
// remote missing cached credentials (or an SSH key waiting on a passphrase)
// hangs forever — a stuck terminal in the CLI, a dead spinner in the app.
const gitTimeout = 60 * time.Second

// configRepo is a temporary client-side clone of a Base's external config
// repo. Call close() to remove the working directory.
type configRepo struct {
	url string
	ref string
	dir string
}

// cloneConfigRepo clones profile's config repo to a fresh temp dir and checks
// out the configured ref. Handles an empty remote (no commits yet) so
// `config setup --init` can seed it.
func cloneConfigRepo(profile vault.Profile) (*configRepo, error) {
	url := strings.TrimSpace(profile.ConfigRepoURL)
	if url == "" {
		return nil, fmt.Errorf("no config repo set for this Base — run `ownbasectl config setup <base> --repo <url>` first")
	}
	ref := profile.EffectiveConfigRef()

	dir, err := os.MkdirTemp("", "ownbase-config-*")
	if err != nil {
		return nil, fmt.Errorf("create temp workdir: %w", err)
	}
	cr := &configRepo{url: url, ref: ref, dir: dir}

	if out, err := cr.git("clone", url, dir); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("clone config repo %s: %w\n%s", url, err, out)
	}
	// Best-effort checkout of the requested ref. An empty remote has no
	// branches yet; the first commit (writeCommitPush) creates ref.
	if out, err := cr.gitIn("rev-parse", "--verify", "--quiet", "origin/"+ref+"^{commit}"); err == nil {
		_ = out
		if out, err := cr.gitIn("checkout", "-B", ref, "origin/"+ref); err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("checkout %s: %w\n%s", ref, err, out)
		}
	} else {
		// No such branch on the remote yet — start it locally.
		_, _ = cr.gitIn("checkout", "-B", ref)
	}
	return cr, nil
}

func (c *configRepo) close() { _ = os.RemoveAll(c.dir) }

// gitEnv returns an environment that never prompts for credentials. The app
// (and unattended CLI runs) cannot answer a password prompt; failing fast
// with a readable error is the only honest outcome.
func gitEnv() []string {
	env := os.Environ()
	env = append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_SSH_COMMAND=ssh -oBatchMode=yes -oStrictHostKeyChecking=accept-new",
	)
	return env
}

func (c *configRepo) git(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("git %s timed out after %s — the remote may need credentials this process does not have (run the equivalent git command once in a terminal, or switch the remote to SSH with an agent key)", args[0], gitTimeout)
	}
	return out, err
}

func (c *configRepo) gitIn(args ...string) ([]byte, error) {
	return c.git(append([]string{"-C", c.dir}, args...)...)
}

// readOwnbaseYAML returns the current ownbase.yaml text, or "" when the file
// does not exist yet (a freshly-seeded/empty config repo).
func (c *configRepo) readOwnbaseYAML() (string, error) {
	data, err := os.ReadFile(filepath.Join(c.dir, ownbaseYAMLName))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", ownbaseYAMLName, err)
	}
	return string(data), nil
}

// writeCommitPush writes content to ownbase.yaml, commits it with msg, and
// pushes to origin at the configured ref. A no-op push (no changes) is
// reported as a distinct error so callers can surface "nothing to do".
func (c *configRepo) writeCommitPush(content, msg string) error {
	if err := os.WriteFile(filepath.Join(c.dir, ownbaseYAMLName), []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", ownbaseYAMLName, err)
	}
	if out, err := c.gitIn("add", ownbaseYAMLName); err != nil {
		return fmt.Errorf("git add: %w\n%s", err, out)
	}
	// Nothing staged means the edit was a no-op.
	if _, err := c.gitIn("diff", "--cached", "--quiet"); err == nil {
		return errNoConfigChange
	}
	if out, err := c.gitIn("-c", "user.name=ownbasectl", "-c", "user.email=ownbasectl@localhost",
		"commit", "-m", msg); err != nil {
		return fmt.Errorf("git commit: %w\n%s", err, out)
	}
	if out, err := c.gitIn("push", "origin", "HEAD:"+c.ref); err != nil {
		return fmt.Errorf("git push to %s: %w\n%s", c.ref, err, out)
	}
	return nil
}

// errNoConfigChange signals that an edit produced no change to ownbase.yaml.
var errNoConfigChange = fmt.Errorf("no change to ownbase.yaml")

// mutateConfig clones the Base's config repo, applies edit to the current
// ownbase.yaml text, validates the result, commits+pushes it, and triggers a
// reconcile on the Base. edit returns the new content and a commit message.
func mutateConfig(base string, edit func(current string) (newContent, commitMsg string, err error)) error {
	_, _, err := mutateConfigInner(base, edit, false)
	return err
}

// configPreview is the dry-run result of an edit: the would-be YAML change and
// the commit message. The app shows this before the user confirms a push.
type configPreview struct {
	Current       string `json:"current"`
	Proposed      string `json:"proposed"`
	Diff          string `json:"diff"`
	CommitMessage string `json:"commit_message"`
	WouldChange   bool   `json:"would_change"`
}

// previewConfig runs edit against a fresh clone and returns the resulting
// diff without committing, pushing, or reconciling. Secrets are never
// written — this is pure config-repo math.
func previewConfig(base string, edit func(current string) (newContent, commitMsg string, err error)) (configPreview, error) {
	p, _, err := mutateConfigInner(base, edit, true)
	return p, err
}

// mutateConfigInner is the shared body of mutateConfig and previewConfig.
// dryRun=true stops after computing the edit.
func mutateConfigInner(base string, edit func(current string) (newContent, commitMsg string, err error), dryRun bool) (configPreview, string, error) {
	profile, err := loadProfile(base)
	if err != nil {
		return configPreview{}, "", err
	}
	cr, err := cloneConfigRepo(profile)
	if err != nil {
		return configPreview{}, "", err
	}
	defer cr.close()

	current, err := cr.readOwnbaseYAML()
	if err != nil {
		return configPreview{}, "", err
	}
	newContent, msg, err := edit(current)
	if err != nil {
		return configPreview{}, "", err
	}
	if _, err := schema.ParseConfig(strings.NewReader(newContent)); err != nil {
		return configPreview{}, "", fmt.Errorf("resulting ownbase.yaml would be invalid: %w", err)
	}

	preview := configPreview{
		Current:       current,
		Proposed:      newContent,
		Diff:          unifiedDiff(ownbaseYAMLName, current, newContent),
		CommitMessage: msg,
		WouldChange:   current != newContent,
	}
	if dryRun {
		return preview, newContent, nil
	}
	if err := cr.writeCommitPush(newContent, msg); err != nil {
		if err == errNoConfigChange {
			return preview, newContent, err
		}
		return preview, newContent, err
	}
	if err := triggerReconcile(base); err != nil {
		return preview, newContent, err
	}
	return preview, newContent, nil
}

// unifiedDiff is a minimal line-oriented diff good enough for a confirm
// dialog. It is not a full Myers diff — added/removed lines only — and that
// is intentional: the app needs something a person can skim, not a patch tool.
func unifiedDiff(name, before, after string) string {
	if before == after {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", name, name)
	beforeLines := strings.Split(before, "\n")
	afterLines := strings.Split(after, "\n")
	// Simple LCS-free presentation: emit the whole file with -/+ markers for
	// lines that differ by index, then any trailing adds/removes. Good enough
	// for the small ownbase.yaml documents we edit.
	n := len(beforeLines)
	if len(afterLines) > n {
		n = len(afterLines)
	}
	for i := 0; i < n; i++ {
		var oldLine, newLine string
		if i < len(beforeLines) {
			oldLine = beforeLines[i]
		}
		if i < len(afterLines) {
			newLine = afterLines[i]
		}
		switch {
		case i >= len(beforeLines):
			fmt.Fprintf(&b, "+%s\n", newLine)
		case i >= len(afterLines):
			fmt.Fprintf(&b, "-%s\n", oldLine)
		case oldLine == newLine:
			fmt.Fprintf(&b, " %s\n", oldLine)
		default:
			fmt.Fprintf(&b, "-%s\n", oldLine)
			fmt.Fprintf(&b, "+%s\n", newLine)
		}
	}
	return b.String()
}

// triggerReconcile asks the Base's daemon to pull the config repo and
// reconcile immediately.
func triggerReconcile(base string) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()
	if _, err := apiCall(conn, http.MethodPost, "/reconcile", nil); err != nil {
		return fmt.Errorf("trigger reconcile: %w", err)
	}
	return nil
}

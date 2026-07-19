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

	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/serverconfig"
)

// ownbaseYAMLName is the config file committed to the config repo.
const ownbaseYAMLName = "ownbase.yaml"

// loadProfile reads the named Base's client profile.
func loadProfile(base string) (serverconfig.ServerProfile, error) {
	cfgPath, err := serverconfig.DefaultConfigPath()
	if err != nil {
		return serverconfig.ServerProfile{}, fmt.Errorf("locate config: %w", err)
	}
	scfg, err := serverconfig.Load(cfgPath)
	if err != nil {
		return serverconfig.ServerProfile{}, fmt.Errorf("load config: %w", err)
	}
	return scfg.ProfileFor(base)
}

// saveProfile persists mutations to the named Base's client profile.
func saveProfile(base string, mutate func(*serverconfig.ServerProfile)) error {
	cfgPath, err := serverconfig.DefaultConfigPath()
	if err != nil {
		return fmt.Errorf("locate config: %w", err)
	}
	scfg, err := serverconfig.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// Only mutate a Base that already exists — otherwise a typo'd name would
	// write a half-populated profile (config repo set, no host/token). The
	// Base must have been registered first (create/adopt).
	p, ok := scfg.Servers[base]
	if !ok {
		return fmt.Errorf("Base %q not found; run: ownbasectl list", base)
	}
	mutate(&p)
	scfg.Servers[base] = p
	return serverconfig.Save(cfgPath, scfg)
}

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
func cloneConfigRepo(profile serverconfig.ServerProfile) (*configRepo, error) {
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

func (c *configRepo) git(args ...string) ([]byte, error) {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	return cmd.CombinedOutput()
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
	profile, err := loadProfile(base)
	if err != nil {
		return err
	}
	cr, err := cloneConfigRepo(profile)
	if err != nil {
		return err
	}
	defer cr.close()

	current, err := cr.readOwnbaseYAML()
	if err != nil {
		return err
	}
	newContent, msg, err := edit(current)
	if err != nil {
		return err
	}
	if _, err := schema.ParseConfig(strings.NewReader(newContent)); err != nil {
		return fmt.Errorf("resulting ownbase.yaml would be invalid: %w", err)
	}
	if err := cr.writeCommitPush(newContent, msg); err != nil {
		if err == errNoConfigChange {
			return err
		}
		return err
	}
	return triggerReconcile(base)
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

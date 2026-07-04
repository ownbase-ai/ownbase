package install

// bootstrap_core.go implements the "core tier" bootstrap that runs during
// pass zero. It starts Forgejo and Caddy as core packages, creates the
// ownbase admin user, generates an API token, writes it to TokenPath, and
// seeds the Forgejo `base` repo with a template ownbase.yaml.
//
// This is the bootstrap exception: Forgejo and Caddy images are pulled from
// their upstream registries exactly once during install. After bootstrap
// they are managed by ownbasectl upgrade.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ownbase/ownbase/internal/core"
	"github.com/ownbase/ownbase/internal/githost"
	"github.com/ownbase/ownbase/internal/schema"
)

// CoreBootstrapConfig configures the core package bootstrap pass.
type CoreBootstrapConfig struct {
	// CoreConfig is the core: block from ownbase.yaml. May be zero-value if
	// the user hasn't configured it yet — defaults are applied.
	CoreConfig schema.CoreConfig

	// BareRepoPath is the path to the on-host bare git repo.
	// Default: /opt/ownbase/repo
	BareRepoPath string

	// TokenPath is where the Forgejo admin token is persisted.
	// Default: /opt/ownbase/forgejo-token (= core.TokenPath)
	TokenPath string

	// ForgejoBaseURL overrides the Forgejo base URL derived from CoreConfig.
	// Use this to pass the container IP directly, bypassing unreliable
	// localhost port forwarding. Leave empty to use CoreConfig defaults.
	ForgejoBaseURL string

	// AgentWebhookURL is the URL the agent listens on for Forgejo push events.
	// Default: "http://host.containers.internal:7070/api/v1/hook/push"
	AgentWebhookURL string

	// AdminPassword is set as the Forgejo owner's login password on first
	// bootstrap. Subsequent runs update it if the value changes.
	// If empty, a random password is generated (token remains the durable
	// agent credential; the password is only needed for the web UI).
	AdminPassword string

	// OwnerSSHKey is the owner's SSH public key to register with Forgejo so
	// they can git push/pull over SSH. Optional — HTTP with token works too.
	// Example: "ssh-ed25519 AAAA... you@laptop"
	// Idempotent: if a key with the same content is already registered, the
	// registration step is skipped.
	OwnerSSHKey string

	// DryRun logs what would be done without making changes.
	DryRun bool
}

func (c *CoreBootstrapConfig) withDefaults() CoreBootstrapConfig {
	cfg := *c
	if cfg.BareRepoPath == "" {
		cfg.BareRepoPath = "/opt/ownbase/repo"
	}
	if cfg.TokenPath == "" {
		cfg.TokenPath = core.TokenPath
	}
	if cfg.AgentWebhookURL == "" {
		cfg.AgentWebhookURL = "http://host.containers.internal:7070/api/v1/hook/push"
	}
	if cfg.AdminPassword == "" {
		cfg.AdminPassword = generateAdminPassword()
	}
	return cfg
}

// DefaultTokenPath is the default location where the Forgejo admin token is
// written by the installer and read by the agent.
const DefaultTokenPath = "/opt/ownbase/forgejo-token"

// BootstrapCore brings up Forgejo and Caddy as core packages (idempotent),
// creates the admin user and API token, seeds the Forgejo `base` repo, and
// configures the push webhook so pushing to Forgejo triggers reconcile.
//
// Steps:
//  1. Start Forgejo container (if not running) from the pinned core manifest.
//  2. Wait for Forgejo to be healthy.
//  3. Create admin user + generate token → write to TokenPath.
//  4. Create `base` repo on Forgejo seeded with template ownbase.yaml.
//  5. Configure push webhook (Forgejo → agent).
//  6. Sync bare repo → Forgejo (initial mirror).
func BootstrapCore(ctx context.Context, cfg CoreBootstrapConfig) error {
	c := cfg.withDefaults()

	forgejoURL := core.ForgejoURL(c.CoreConfig)
	if c.ForgejoBaseURL != "" {
		forgejoURL = c.ForgejoBaseURL
	}
	port := c.CoreConfig.Forgejo.EffectivePort()

	// Step 1: Ensure Forgejo container is running.
	if err := ensureForgejoRunning(ctx, c, port); err != nil {
		return fmt.Errorf("bootstrap core: start forgejo: %w", err)
	}

	// Step 2: Wait for healthy.
	const timeout = 3 * time.Minute
	if err := githost.WaitForForgejo(forgejoURL, core.ForgejoContainerName, timeout); err != nil {
		return fmt.Errorf("bootstrap core: forgejo not healthy: %w", err)
	}

	// Step 3: Create admin + token. The admin user creation is idempotent.
	if err := githost.CreateForgejoAdmin(
		core.ForgejoContainerName,
		core.AdminUser,
		c.AdminPassword,
		core.AdminEmail,
	); err != nil {
		return fmt.Errorf("bootstrap core: create admin: %w", err)
	}

	// Check if the token already exists; only generate once.
	token, err := readTokenFile(c.TokenPath)
	if err != nil {
		// Generate a fresh token.
		token, err = githost.GenerateForgejoToken(
			core.ForgejoContainerName,
			core.AdminUser,
			"ownbased",
		)
		if err != nil {
			return fmt.Errorf("bootstrap core: generate token: %w", err)
		}
		if !c.DryRun {
			if err := writeTokenFile(c.TokenPath, token); err != nil {
				return fmt.Errorf("bootstrap core: write token: %w", err)
			}
		}
	}

	fgCfg := githost.ForgejoConfig{
		BaseURL:    forgejoURL,
		AdminToken: token,
		RepoOwner:  core.AdminUser,
		RepoName:   githost.DefaultForgejoRepoName,
	}

	// Step 3a: Set the owner's web-UI password (idempotent — update on every run
	// so a changed password in first-run.env is applied even after re-install).
	if !c.DryRun && c.AdminPassword != "" {
		if err := githost.UpdateForgejoUserPassword(fgCfg, core.AdminUser, c.AdminPassword); err != nil {
			// Non-fatal: the token is the durable agent credential.
			fmt.Printf("bootstrap core: set owner password (non-fatal): %v\n", err)
		}
	}

	// Step 3b: Register the owner's SSH public key if one was provided.
	if !c.DryRun && c.OwnerSSHKey != "" {
		if err := githost.RegisterForgejoSSHKey(fgCfg, c.OwnerSSHKey, "owner"); err != nil {
			fmt.Printf("bootstrap core: register ssh key (non-fatal): %v\n", err)
		}
	}

	// Step 4: Create `base` repo if it doesn't exist.
	if _, err := githost.CreateForgejoRepo(fgCfg); err != nil {
		// CreateForgejoRepo is expected to be idempotent (no-op if exists).
		// Log but don't fail.
		_ = err
	}

	// Seed template ownbase.yaml if the repo is brand new.
	if !c.DryRun {
		tmpl := buildTemplateOwnbaseYAML(c.CoreConfig.Forgejo.Domain, c.CoreConfig.Caddy.Email)
		if err := githost.SeedBaseRepo(fgCfg, tmpl); err != nil {
			return fmt.Errorf("bootstrap core: seed base repo: %w", err)
		}
	}

	// Step 5: Configure push webhook.
	if !c.DryRun {
		if err := githost.ConfigureForgejoWebhook(fgCfg, c.AgentWebhookURL); err != nil {
			// Non-fatal: webhook configuration failure doesn't stop the installer.
			fmt.Printf("bootstrap core: configure webhook (non-fatal): %v\n", err)
		}
	}

	// Step 6: Initial sync from bare repo to Forgejo.
	// This is a best-effort push; it may fail if the bare repo is empty.
	if !c.DryRun && c.BareRepoPath != "" {
		if err := githost.AddForgejoRemote(c.BareRepoPath, fgCfg); err != nil {
			fmt.Printf("bootstrap core: add forgejo remote (non-fatal): %v\n", err)
		} else if err := githost.SyncToForgejo(c.BareRepoPath); err != nil {
			fmt.Printf("bootstrap core: initial sync (non-fatal): %v\n", err)
		}
	}

	return nil
}

// ReadForgejoToken reads the agent token from the well-known token path.
// Returns an error if the file doesn't exist or is empty (Forgejo not
// bootstrapped yet).
func ReadForgejoToken(tokenPath string) (string, error) {
	if tokenPath == "" {
		tokenPath = core.TokenPath
	}
	token, err := readTokenFile(tokenPath)
	if err != nil {
		return "", fmt.Errorf("read forgejo token from %s: %w — has ownbase been installed?",
			tokenPath, err)
	}
	return token, nil
}

func ensureForgejoRunning(ctx context.Context, cfg CoreBootstrapConfig, port int) error {
	// Check if the container is already running.
	out, err := exec.CommandContext(ctx,
		"podman", "inspect", "--format", "{{.State.Running}}", core.ForgejoContainerName,
	).Output()
	if err == nil && strings.TrimSpace(string(out)) == "true" {
		return nil // already running
	}

	if cfg.DryRun {
		fmt.Printf("would start %s container\n", core.ForgejoContainerName)
		return nil
	}

	m := core.Current
	image := m.ForgejoImage
	if m.ForgejoDigest != "" {
		image = m.ForgejoImage + "@" + m.ForgejoDigest
	}

	env := core.ForgejoEnvForContainer(port, cfg.CoreConfig.Forgejo.Domain)
	args := []string{
		"run", "-d",
		"--name", core.ForgejoContainerName,
		"--restart", "always",
		"-v", core.ForgejoDataVolume + ":/data",
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", port, port),
	}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, image)

	if out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("podman run forgejo: %w\n%s", err, out)
	}
	return nil
}

func readTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("empty token file at %s", path)
	}
	return token, nil
}

func writeTokenFile(path string, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(token+"\n"), 0o600)
}

// generateAdminPassword generates a random-looking password for the Forgejo
// admin. The admin never logs in with the password directly — only the token
// is used. This just prevents the Forgejo setup page from being accessible.
func generateAdminPassword() string {
	return "ownbase-" + fmt.Sprintf("%d", time.Now().UnixNano())
}

// buildTemplateOwnbaseYAML generates the template ownbase.yaml committed to the
// Forgejo base repo during fresh installs. When domain and email are provided
// (passed at install time via FORGEJO_DOMAIN / CADDY_EMAIL), they are written
// as active values so the first reconcile configures Caddy/TLS immediately.
// Otherwise the fields are left as commented-out examples.
func buildTemplateOwnbaseYAML(domain, email string) string {
	forgejoLine := "    # domain: git.mysite.com  # optional: public hostname for the git UI"
	if domain != "" {
		forgejoLine = "    domain: " + domain
	}
	caddyLine := "    # email: you@example.com  # for automatic TLS certificates"
	if email != "" {
		caddyLine = "    email: " + email
	}
	return `schema_version: v1

# OwnBase configuration — the single source of truth for everything running
# on this Base. Edit this file, commit, and push to apply changes.
#
# The reconciler watches this file; every push triggers a reconcile.

# core: configures OwnBase infrastructure (Forgejo git host, Caddy proxy).
# Versions are managed by 'ownbasectl upgrade' — do not set them here.
core:
  forgejo:
` + forgejoLine + `
  caddy:
` + caddyLine + `

# services: declares applications to build and run on this Base.
# Every service is built from a local Forgejo repo or a mirrored external repo.
# The Dockerfile in the repo is the build interface — no separate manifest needed.
#
# Example source service (repo lives on this Forgejo):
#   myapp:
#     source: services/myapp     # repo path relative to Forgejo
#     ref: v1.0.0                # pin to a tag/branch/SHA (omit to auto-pin to latest)
#     port: 8080
#     domain: myapp.example.com
#     requires:
#       - postgres               # joins the postgres capability network
#
# All traffic from the public domain routes to the service port.
# The data_path field sets the container mount for the persistent data volume
# (default: /data). The volume itself is always ownbase-<name>-data.

services: {}
# Add services here. Example (uncomment and customise):
#
#   postgres:
#     mirror: https://github.com/docker-library/postgres
#     context: "17/alpine3.23"    # check the repo for current path
#     port: 5432
#     data_path: /var/lib/postgresql/data
#
#   myapp:
#     source: services/myapp       # repo on this Forgejo
#     ref: v1.0.0
#     port: 8080
#     domain: myapp.example.com
`
}

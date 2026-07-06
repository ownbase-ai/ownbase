package install

// seed.go seeds a fresh config repo checkout with a template ownbase.yaml and
// operating README on first install, then commits and pushes the result to
// the local bare repo (its origin remote). This replaces the old Forgejo
// bootstrap's "seed the base repo via API" step: there is no API anymore,
// just a local git commit + push, exactly like any other config change.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// SeedConfigRepo writes a template ownbase.yaml and README.md into checkoutPath
// and commits + pushes them to the bare repo, but only if ownbase.yaml does not
// already exist there. Idempotent: a no-op on every run after the first.
//
// caddyEmail, when non-empty (from first-run.env), is pre-filled into the
// template so the very first reconcile can configure TLS without a manual edit.
func SeedConfigRepo(checkoutPath, caddyEmail string) error {
	cfgPath := filepath.Join(checkoutPath, "ownbase.yaml")
	if _, err := os.Stat(cfgPath); err == nil {
		return nil // already seeded (or user pushed their own config first)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", cfgPath, err)
	}

	if err := os.WriteFile(cfgPath, []byte(buildTemplateOwnbaseYAML(caddyEmail)), 0o644); err != nil {
		return fmt.Errorf("write template ownbase.yaml: %w", err)
	}
	readmePath := filepath.Join(checkoutPath, "README.md")
	if err := os.WriteFile(readmePath, []byte(baseRepoReadme), 0o644); err != nil {
		return fmt.Errorf("write README.md: %w", err)
	}

	if out, err := exec.Command("git", "-C", checkoutPath, "add", "ownbase.yaml", "README.md").CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", checkoutPath, "commit", "-m", "init: seed ownbase.yaml").CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", checkoutPath, "push", "origin", "HEAD").CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %w\n%s", err, out)
	}
	return nil
}

// buildTemplateOwnbaseYAML generates the template ownbase.yaml seeded into a
// fresh config repo. When caddyEmail is provided (from first-run.env, set at
// install time via CADDY_EMAIL), it is written as an active value so the
// first reconcile configures TLS immediately. Otherwise it is left as a
// commented-out example.
func buildTemplateOwnbaseYAML(caddyEmail string) string {
	caddyLine := "    # email: you@example.com  # for automatic TLS certificates"
	if caddyEmail != "" {
		caddyLine = "    email: " + caddyEmail
	}
	return `schema_version: v1

# OwnBase configuration — the single source of truth for everything running
# on this Base. Edit this file, commit, and push (or use ownbasectl config
# set / service add) to apply changes.
#
# The reconciler watches this file; every push triggers a reconcile.

# core: configures OwnBase infrastructure (the Caddy reverse proxy).
# Versions are managed by 'ownbasectl upgrade' — do not set them here.
core:
  caddy:
` + caddyLine + `

# services: declares applications to build and run on this Base.
# Every service is built from its own local bare repo under
# /opt/ownbase/repos/ — either one you push into directly over SSH (source:)
# or one OwnBase clones and tracks from an external URL (mirror:). The
# Dockerfile in that repo is the build interface — no separate manifest needed.
#
# Example source service (its own bare repo, pushed to directly over SSH —
# see 'ownbasectl service add' to create it):
#   myapp:
#     source: services/myapp     # name of the bare repo under /opt/ownbase/repos/
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
# Add services here. Example (uncomment and customise), or use
# 'ownbasectl service add <base> <name> ...':
#
#   postgres:
#     mirror: https://github.com/docker-library/postgres
#     context: "17/alpine3.23"    # check the repo for current path
#     port: 5432
#     data_path: /var/lib/postgresql/data
#
#   myapp:
#     source: services/myapp       # bare repo pushed to directly over SSH
#     ref: v1.0.0
#     port: 8080
#     domain: myapp.example.com
`
}

// baseRepoReadme is the operating guide seeded as README.md in the config
// repo. It is the first thing a human or AI sees when they open the repo, so
// it carries the full operating contract. It is static: seeded once at
// bootstrap, never regenerated (live status belongs to ownbasectl and the
// status API, not to git).
const baseRepoReadme = `# This Base's configuration repository

This repo is the source of truth for everything running on this Base.
` + "`ownbase.yaml`" + ` declares every service; the OwnBase daemon watches this repo
and reconciles the machine to match it. Any tool that can read a file and
make a commit — human or AI — can operate this Base safely from here.

## How to make a change

1. Edit ` + "`ownbase.yaml`" + ` (or use ` + "`ownbasectl config set` / `ownbasectl service add`" + `)
   to add, remove, or reconfigure a service.
2. To update a service, change its ` + "`ref:`" + ` to the new branch, tag, or commit.
3. Commit and push (over SSH, directly to this bare repo). The daemon detects
   the push and converges the machine automatically — build from source,
   health-gated start, seconds not minutes.
4. **Never edit generated files under ` + "`runtime/`" + ` on the Base** — they are
   compiler output; any hand-edit is overwritten and flagged as a tamper signal.
5. To add a new service: run ` + "`ownbasectl service add`" + ` to create its bare
   repo under ` + "`/opt/ownbase/repos/`" + ` (source:) or point at an external git
   URL OwnBase should mirror (mirror:), then push a Dockerfile into it. The
   Dockerfile is the only build interface.

## Reading a service entry

` + "```yaml" + `
services:
  myapp:
    source: services/myapp   # name of the bare repo under /opt/ownbase/repos/
    ref: v1.0.0              # the exact version running (edit + push to update)
    port: 8080               # container port; public traffic routes here
    domain: myapp.example.com # public hostname (TLS is automatic)
    requires:
      - postgres             # reachable at hostname "postgres" from this service
` + "```" + `

A service reaches each capability it ` + "`requires:`" + ` by using the capability name
as the hostname (e.g. connect to ` + "`postgres:5432`" + `).

## What does not live in this repo

- **Live status** (running/stopped, health, security posture, update drift):
  run ` + "`ownbasectl status <base>`" + ` or ` + "`ownbasectl checkup <base>`" + `.
- **Secrets**: never commit them here. They are stored encrypted on the Base
  and injected at container start — manage with ` + "`ownbasectl secrets`" + `.

## Reference documentation

| Need | Doc |
|---|---|
| Full operating playbook | [docs/operating.md](https://github.com/ownbase-ai/ownbase/blob/main/docs/operating.md) |
| ` + "`ownbase.yaml`" + ` schema, ` + "`ref:`" + ` updates, secrets | [docs/ownbase-yaml.md](https://github.com/ownbase-ai/ownbase/blob/main/docs/ownbase-yaml.md) |
| CLI command reference | [docs/cli.md](https://github.com/ownbase-ai/ownbase/blob/main/docs/cli.md) |
| Daemon HTTP API | [docs/api.md](https://github.com/ownbase-ai/ownbase/blob/main/docs/api.md) |
| Adding a service | [docs/integration-contract.md](https://github.com/ownbase-ai/ownbase/blob/main/docs/integration-contract.md) |
| Something failed | [docs/troubleshooting.md](https://github.com/ownbase-ai/ownbase/blob/main/docs/troubleshooting.md) |
| Exporting everything / retiring | [docs/uninstall.md](https://github.com/ownbase-ai/ownbase/blob/main/docs/uninstall.md) |

This file was seeded by OwnBase at install time and is yours to edit.
`

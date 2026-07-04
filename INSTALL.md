# INSTALL.md

> How to set up a Base, and how to verify a fresh install works end-to-end.

Setup is driven entirely by `ownbasectl`. The same command works whether the Base is a **local Multipass VM** or a **remote Ubuntu server**; only one flag differs.

---

## Prerequisites

- `ownbasectl` installed (next section).
- **Local VM path:** [Multipass](https://multipass.run) installed (`brew install --cask multipass` on macOS; see the [Multipass docs](https://multipass.run/install) for Linux). Works on both macOS and Linux hosts.
- **Remote server path:** a fresh Ubuntu 22.04/24.04 machine reachable over SSH as `root` (or a sudo-capable user), and an SSH key already authorized on it.

No Go toolchain and no cloned repo are needed — those are only for contributors (see [Contributors: running from source](#contributors-running-from-source)).

---

## Install ownbasectl

```bash
brew install --cask ownbase-ai/tap/ownbasectl
```

Or download the archive for your platform from [GitHub Releases](https://github.com/ownbase-ai/ownbase/releases), unpack it, and put `ownbasectl` on your `PATH`. Verify with:

```bash
ownbasectl version
```

---

## Set up a new Base

### 1. Create it

```bash
# Local VM (default)
ownbasectl create mybase

# Remote server
ownbasectl create mybase --remote root@mybase.example.com \
  --forgejo-domain git.yourdomain.com --caddy-email you@example.com
```

What `create` does, in order:

1. Provisions the target — launches a fresh Ubuntu 24.04 VM via Multipass (deleting any existing VM with the same name first), or connects over SSH to the server you provisioned.
2. Uploads the installer (embedded in `ownbasectl`) and runs it as root: it downloads the `ownbased` daemon release matching your `ownbasectl` version, verifies its minisign signature, then runs pass zero (Podman, UFW, fail2ban, unattended-upgrades, trivy) and the Forgejo + Caddy bootstrap.
3. Reads the generated API token back and registers the Base as `mybase` in `~/.ownbase/config` — nothing to copy-paste.

Omit `--forgejo-domain`/`--caddy-email` if you don't have a domain yet; Forgejo is then reached directly at `http://<host>:3000`.

```bash
ownbasectl status mybase
ownbasectl forgejo mybase
```

### 2. Set up remote backups

```bash
ownbasectl backup setup mybase \
  --repo s3:s3.amazonaws.com/my-bucket/ownbase \
  --password <a-strong-restic-password> \
  --aws-access-key-id AKIA... --aws-secret-access-key ...
```

This is a standard part of setup, not an optional extra — see [`backup setup` in the CLI reference](docs/cli.md#backup-setuprunstatus-name) for the full picture, including B2/SFTP repos and how the verified-restore drill works.

### 3. Recurring health check

```bash
ownbasectl checkup mybase
```

Run this regularly (weekly is reasonable). It combines intrusion/access monitoring, network exposure, CVE scan results, service update drift, and backup health into one report, with the exact fix command next to each finding.

### 4. Disaster recovery — restore onto a new machine

```bash
ownbasectl restore mybase \
  --repo s3:s3.amazonaws.com/my-bucket/ownbase \
  --password <the-restic-password>
```

Provisions a fresh VM (or `--remote <host>` for a fresh server), runs the installer in rebuild mode, restores the latest verified snapshot, and lets the daemon's normal reconcile take it from there.

### Managing multiple Bases

```bash
ownbasectl list             # profiles + local VMs
ownbasectl delete mybase    # tear down the local VM + its profile
```

---

## Contributors: running from source

Everything above also works from a checkout of this repo without installing a release:

```bash
go run ./cmd/ownbasectl create mybase
```

A dev build (version `dev`) behaves differently in exactly one way: `create` with no `--remote` (the local VM path) cross-compiles `ownbased` from the checkout (`go build -tags=integration`, GOARCH matched to the host) and transfers the binary directly into the VM — no HTTP release server needed, and the daemon under test is your working tree, not a release. `create --remote` uses the signed-release download path either way (a dev build installs the latest release rather than a pinned version).

---

## Developer: verifying a fresh install end-to-end

The steps above are what a user runs. This section is for verifying the installer itself still works correctly after changing `install.sh`, the daemon's bootstrap path, or `internal/vmhost` — it is separate from the automated test suite described in [AGENTS.md](AGENTS.md), because the fresh-install path (pass zero → Quadlet bootstrap → Forgejo → reconcile loop) cannot be fully exercised by unit or integration tests; it requires a real installer run on a clean machine.

### Run it

```bash
go run ./cmd/ownbasectl create ownbase-fresh
# equivalent to: make smoke-test
```

`make smoke-test` and `make connect-vm` are now thin aliases for this same command — the daemon binary is built fresh from this checkout every run, and the resulting profile is registered automatically, so there is no separate "connect" step anymore. `create` always deletes any existing VM with the same name before launching, so re-running it is already "provision a clean VM" — no separate `multipass delete`/`launch` step needed.

### Watch the daemon

```bash
multipass shell ownbase-fresh
sudo journalctl -u ownbased -f
```

### What a successful install looks like

```
pass zero complete — host is hardened
bootstrap core: ...                      ← Quadlet units written, SIGHUP fired
starting (mode=integration ...)          ← real Podman+Quadlet mode
using Forgejo token from /opt/ownbase/forgejo-token
already converged — no changes needed
update detection enabled ...
```

### Verify after startup

```bash
# Get the VM IP
multipass info ownbase-fresh | grep IPv4

# From your Mac — Forgejo is at http://<VM-IP>:3000
curl -s http://<VM-IP>:3000/api/healthz | python3 -m json.tool

# Or open a VM shell and check from inside
multipass exec ownbase-fresh -- curl -s http://localhost:3000/api/healthz | python3 -m json.tool
multipass exec ownbase-fresh -- sudo podman ps                  # both forgejo and caddy running
multipass exec ownbase-fresh -- sudo systemctl list-units 'ownbase-*'   # 4 units loaded
multipass exec ownbase-fresh -- sudo ls /etc/containers/systemd/        # Quadlet unit files

# Verify trivy was installed by PassZero
multipass exec ownbase-fresh -- trivy --version
```

### Then use `ownbasectl` as usual

```bash
go run ./cmd/ownbasectl status ownbase-fresh
go run ./cmd/ownbasectl checkup ownbase-fresh
go run ./cmd/ownbasectl forgejo ownbase-fresh
```

---

## Agent-level bootstrap tests

These tests exercise `bootstrapCore` directly — the Quadlet installation, SIGHUP reload, and `systemctl start` path that the E2E tests in `internal/install/` do not cover. Run them on `ownbase-test` (not `ownbase-fresh`, which has a live daemon using the same container names).

```bash
# Sync the latest code first
make sync-vm VM=ownbase-test

# Run
multipass exec ownbase-test -- bash -c \
  'cd ~/ownbase && sudo /usr/local/go/bin/go test -tags=integration -count=1 \
   ./cmd/ownbased/... -v -timeout 10m'
```

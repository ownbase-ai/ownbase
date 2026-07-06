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

### Homebrew (macOS/Linux)

```bash
brew install --cask ownbase-ai/tap/ownbasectl
```

### Without Homebrew

Downloads the latest release for your OS/arch, verifies its checksum against the release's `checksums.txt`, and installs it to `/usr/local/bin`:

```bash
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"; case "$ARCH" in x86_64) ARCH=amd64 ;; arm64|aarch64) ARCH=arm64 ;; esac

TAG="$(curl -fsSL https://api.github.com/repos/ownbase-ai/ownbase/releases/latest | grep '"tag_name"' | cut -d'"' -f4)"
FILE="ownbasectl_${TAG#v}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/ownbase-ai/ownbase/releases/download/${TAG}"

cd "$(mktemp -d)"
curl -fsSLO "${BASE_URL}/${FILE}" && curl -fsSLO "${BASE_URL}/checksums.txt"
grep " ${FILE}\$" checksums.txt | (command -v sha256sum >/dev/null && sha256sum -c - || shasum -a 256 -c -)

tar xzf "$FILE" ownbasectl
sudo install -m 0755 ownbasectl /usr/local/bin/ownbasectl
```

Supported platforms: macOS and Linux, each on amd64/arm64. There's no pre-built package for other package managers (apt, etc.) yet — this script and the Homebrew cask are the two supported paths.

> **Downloaded via a browser instead?** macOS Gatekeeper quarantines browser downloads (it doesn't quarantine plain `curl`/`wget` downloads, so the script above is unaffected). If you see "cannot be opened because the developer cannot be verified" — the binaries aren't Apple-notarized, and only the Homebrew cask strips the quarantine flag automatically — clear it yourself: `xattr -dr com.apple.quarantine /usr/local/bin/ownbasectl`.

Verify either install method with:

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
  --caddy-email you@example.com
```

What `create` does, in order:

1. Provisions the target — launches a fresh Ubuntu 24.04 VM via Multipass (deleting any existing VM with the same name first), or connects over SSH to the server you provisioned.
2. Uploads the installer (embedded in `ownbasectl`) and runs it as root: it downloads the `ownbased` daemon release matching your `ownbasectl` version, verifies its minisign signature, then runs pass zero (Podman, UFW, fail2ban, unattended-upgrades, trivy) and seeds the local config bare repo with a starter `ownbase.yaml`.
3. Reads the generated API token back and registers the Base as `mybase` in `~/.ownbase/config` — nothing to copy-paste.

`--caddy-email` is only needed if you're putting services on public domains with automatic TLS; omit it otherwise.

```bash
ownbasectl status mybase
ownbasectl config get mybase
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

### 4. Disaster recovery — rebuild after losing the machine

Before tearing down a Base, confirm you have a verified restore point — `restore` refuses unverified snapshots without `--force`:

```bash
ownbasectl backup status mybase   # want "restorable: true"
```

Then, whether the machine was lost or you deleted it yourself (`ownbasectl delete mybase`), rebuild onto a fresh VM or server with the same repo and password:

```bash
ownbasectl restore mybase \
  --repo s3:s3.amazonaws.com/my-bucket/ownbase \
  --password <the-restic-password>
```

This provisions a fresh VM (or `--remote <host>` for a fresh server), runs the installer in rebuild mode, restores the latest verified snapshot — which includes the Base's own Git repo, not just service data — and lets the daemon's normal reconcile take it from there.

### Pausing a local VM

`create`/`delete` are the only VM lifecycle `ownbasectl` manages directly. To pause a local VM between sessions without losing anything, use Multipass itself — the Base and its data are untouched:

```bash
multipass stop mybase
multipass start mybase
```

Multipass may hand the VM a new IP on restart. If `ownbasectl status mybase` stops connecting afterward:

```bash
multipass info mybase | grep IPv4
ownbasectl adopt mybase --host <new-ip> --token <token>   # token: `sudo cat /opt/ownbase/api-token` on the VM
```

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

The steps above are what a user runs. Verifying the installer itself after changing `install.sh`, the daemon's bootstrap path, or `internal/vmhost` — including the fresh-install smoke test and the agent-level bootstrap tests — is covered in [docs/development.md](docs/development.md).

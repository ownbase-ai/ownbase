# Installing OwnBase

> The reference behind the setup walkthrough in [README.md](README.md). Start there for the happy path; come here for the local VM, the manual install, unusual servers, and what to do when something fails.

---

## Install `ownbasectl`

`ownbasectl` runs on your own computer — macOS or Linux, amd64 or arm64. Nothing needs to be installed on the server by hand.

### Homebrew

```bash
brew install --cask ownbase-ai/tap/ownbasectl
```

### Without Homebrew

Downloads the latest release for your OS and architecture, verifies its checksum against the release's `checksums.txt`, and installs to `/usr/local/bin`:

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

There is no apt or other package-manager build yet; the cask and this script are the two supported paths.

> **Downloaded through a browser instead?** macOS Gatekeeper quarantines browser downloads and the binaries are not Apple-notarized, so you will see "cannot be opened because the developer cannot be verified". Only the Homebrew cask clears the quarantine flag automatically. Clear it yourself with `xattr -dr com.apple.quarantine /usr/local/bin/ownbasectl`. Plain `curl` and `wget` downloads are not quarantined, so the script above is unaffected.

Verify either method:

```bash
ownbasectl version
```

---

## Trying it on a local VM

A local VM needs no server, no provider, and no SSH key. It is the fastest way to see what a Base is.

Requires [Multipass](https://multipass.run) (`brew install --cask multipass` on macOS; see the [Multipass docs](https://multipass.run/install) for Linux).

```bash
ownbasectl create mybase
ownbasectl status mybase
```

`create` launches a fresh Ubuntu 24.04 VM, deleting any existing VM of the same name first — it will ask before doing so. Size it with `--cpus`, `--memory`, `--disk` (defaults: 2 CPUs, 2 GB, 15 GB).

You do not need to run `keygen` first: there is no provider to authorize a key with, so if you have no SSH key `create` generates one at `~/.ssh/ownbase_<name>` and the VM boots with it authorized.

Since a local VM has no DNS records, Caddy never gets a real certificate. Use `ownbasectl tunnel mybase` to reach services at trusted local HTTPS URLs.

### Pausing a local VM

`create` and `delete` are the only VM lifecycle operations `ownbasectl` manages. To pause between sessions, use Multipass directly — the Base and its data are untouched:

```bash
multipass stop mybase
multipass start mybase
```

Multipass may assign a new IP on restart. If `ownbasectl status mybase` stops connecting afterward:

```bash
multipass info mybase | grep IPv4
ownbasectl adopt mybase --host <new-ip> --token <token>   # token: sudo cat /opt/ownbase/api-token
```

---

## Installing on a server

The walkthrough is in [README.md](README.md#setting-up-a-base). This is the reference for what `create` requires, what it does, and how to handle servers that differ from the default.

### What the server must be

| Requirement | Detail |
|---|---|
| OS | Ubuntu 22.04 or 24.04, a stock provider image |
| Architecture | x86_64 or aarch64 |
| Memory | 2 GB minimum — see [sizing](README.md#how-big-a-machine) |
| Disk | 20 GB minimum |
| Access | SSH as root, or as a user with passwordless `sudo` |
| Network | Outbound HTTPS, to fetch the daemon release and build service images |

`create` verifies every one of these before it changes anything on the machine, so a mismatch costs you an error message rather than a half-configured server.

### What `create --remote` does, in order

1. **Waits for SSH.** A freshly created cloud server refuses connections for anywhere from ten seconds to two minutes after the provider's console says "running". Controlled by `--wait-for-ssh` (default 5m).
2. **Runs preflight.** Passwordless sudo, Ubuntu version, architecture, memory, disk. Fails here, before any change, with a message naming the specific problem.
3. **Uploads and runs the installer.** It downloads the `ownbased` release matching your `ownbasectl` version, verifies its minisign signature, creates the `ownbase` system user, and installs the systemd unit.
4. **Registers the Base** in `~/.ownbase/config`, reading the generated API token back over SSH. Nothing to copy or paste.
5. **Waits for hardening,** with `--wait`. The daemon runs pass zero — Podman, UFW, fail2ban, unattended-upgrades, Trivy — on startup, *before* it binds its API port. So the API answering is exactly the signal that hardening finished.

Without `--wait`, `create` returns after step 4 and the daemon keeps working in the background for another minute or two.

### Useful flags

| Flag | Purpose |
|---|---|
| `--wait` | Block until the host is hardened and the daemon is healthy |
| `--json` | Machine-readable result instead of the banner |
| `--caddy-email` | ACME contact for automatic TLS; only needed for public domains |
| `--ssh-user` | Login user when it is not root. Needs passwordless sudo |
| `--ssh-key` | Override key selection (default: the `keygen` key for this Base, else `~/.ssh/id_ed25519`) |
| `--ssh-port` | Non-standard SSH port. Also tells the daemon which port to open in UFW and jail in fail2ban |
| `--replace` | Allow an existing Base name to be repointed at a different machine |
| `--wait-for-ssh` | How long to wait for a booting server (default 5m) |

### Exit codes

`create` and `restore` classify their failures so an unattended caller can react:

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Unclassified failure |
| 2 | Bad flags or arguments |
| 3 | Preflight failed — the target was unreachable or unfit. Nothing was changed |
| 4 | The installer ran and failed |
| 5 | Installed, but not healthy within `--wait-timeout` |
| 6 | Refused — valid command, but it would have discarded another Base's API token. Nothing was changed |

### Two different SSH keys

Easy to conflate, so worth stating plainly:

| | Direction | Created by | Private half lives |
|---|---|---|---|
| **Owner key** | your machine → the Base | `ownbasectl keygen <name>` | on your machine |
| **Deploy key** | the Base → GitHub | `ownbasectl ssh-key add <name>` | on the Base, never leaves it |

The owner key must exist before the server does, because the provider authorizes it at creation time. The deploy key is created afterward and registered read-only on your repos.

---

## Rebuilding after losing a machine

Confirm you have a verified restore point first — `restore` refuses an unverified snapshot without `--force`:

```bash
ownbasectl backup status mybase   # want "restorable: true"
```

Then rebuild onto a fresh VM or server with the same repository and password:

```bash
ownbasectl restore mybase \
  --repo s3:s3.amazonaws.com/my-bucket/ownbase \
  --password <the-restic-password>
```

This provisions the target (add `--remote <host>` for a server), runs the installer in rebuild mode, restores the latest verified snapshot — the age key, secrets, and service data included — and lets the daemon's normal reconcile bring every service back.

Restore is also how you move to a bigger machine: it expects to point the Base's name at a new host, so no `--replace` is needed.

## Managing multiple Bases

```bash
ownbasectl list             # profiles + local VMs
ownbasectl delete mybase    # tear down the local VM and its profile
```

---

## When setup fails

[docs/troubleshooting.md](docs/troubleshooting.md) is organized by symptom and covers install failures, SSH and tunnel problems, lost tokens, Multipass quirks, and restic errors.

The daemon's journal is the canonical diagnostic for anything that happens after the installer finishes:

```bash
ssh root@<host> journalctl -u ownbased -n 100
```

---

## Contributors: running from source

Everything above works from a checkout without installing a release:

```bash
go run ./cmd/ownbasectl create mybase
```

A dev build (version `dev`) differs in exactly one way: `create` with no `--remote` cross-compiles `ownbased` from the checkout and transfers the binary straight into the VM, so the daemon under test is your working tree rather than a release. `create --remote` uses the signed-release download path either way, installing the latest release rather than a pinned version.

Verifying the installer itself after changing `install.sh`, the daemon's bootstrap path, or `internal/vmhost` is covered in [docs/development.md](docs/development.md).

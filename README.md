# OwnBase

> AI makes it easy to build software. OwnBase makes it easy to own it.

Grab a server or VM (cloud, home lab, anywhere) and OwnBase turns it into a **Base**: a hardened, self-updating home for your entire application layer. Everything above the OS is yours: services, databases, data, all running on hardware you control, with no third-party platform standing in between.

🫵 Your server &nbsp; 🫵 Your software &nbsp; 🚫 No subscriptions

## Why you'd want this

- **You own everything.** The code, the server, the data, the config, the secrets, the backups, the domains. Nothing can be repriced, rate-limited, or shut down on you — and when you want to leave, you take all of it ([docs/uninstall.md](docs/uninstall.md)).
- **Your AI builds faster here.** Behind a platform API, your AI is a tenant; on a Base, it has the real machine. It can debug like an engineer, run any open-source software, stand up its own databases, and operate everything without guessing — the config repo's README carries the full operating contract, and one command returns the live status.
- **The sysadmin work disappears.** Firewall, intrusion protection, automatic security updates, TLS certificates, CVE scanning: set up during `create`, maintained by the daemon after. `ownbasectl checkup` tells you in plain language whether anything needs a glance.
- **Your data is provably safe.** Encrypted off-machine snapshots every hour, and a daily drill that _actually restores_ the latest backup and checks it. "Restorable" is a measured fact, not a checkbox.
- **One machine replaces a pile of subscriptions.** Auth, databases, job queues, and every app you build run together on one modest box — one predictable bill instead of a per-seat, per-usage sprawl.
- **No lock-in, structurally.** Every service is removable, forkable, and replaceable; config is plain files in a Git repo you own; uninstalling OwnBase leaves a working, still-hardened Ubuntu machine behind.

---

## Walkthrough: zero to a running, backed-up service

Everything below is driven by `ownbasectl`, the CLI you run on your own machine. Each step builds on the last; a fresh Base with no services is safe (it exposes nothing but SSH), so nothing here is riskier than reading it. For the full reference and edge cases, see [INSTALL.md](INSTALL.md) and [docs/cli.md](docs/cli.md).

### 1. Install `ownbasectl`

```bash
brew install --cask ownbase-ai/tap/ownbasectl
```

No Homebrew? A manual install script (verifies the release checksum, no Go toolchain needed) is in [INSTALL.md](INSTALL.md#install-ownbasectl). Verify either way with `ownbasectl version`.

### 2. Create a Base

```bash
# Local VM, for trying things out (needs Multipass: brew install --cask multipass)
ownbasectl create mybase

# ...or a fresh Ubuntu 22.04/24.04 server you already provisioned
ownbasectl create mybase --remote root@mybase.example.com \
  --caddy-email you@example.com
```

One command: provisions the target, hardens it (Podman, UFW, fail2ban, unattended-upgrades, CVE scanning), installs and verifies the signed `ownbased` daemon, bootstraps a local config repo with a starter `ownbase.yaml`, and registers the Base in `~/.ownbase/config` — nothing to copy-paste. A freshly created Base has no domain configured anywhere, so it exposes nothing but SSH externally.

```bash
ownbasectl status mybase       # confirm it's up
ownbasectl config get mybase   # see the starter ownbase.yaml
```

### 3. Add a service

Declare a service with one command — the daemon creates it and builds it from source, health-gated. [`traefik/whoami`](https://github.com/traefik/whoami) is a good first service to try: a tiny, dependency-free Go web server (commonly used to smoke-test reverse proxies) that just echoes back request info, with a `Dockerfile` already at the repo root:

```bash
ownbasectl service add mybase hello \
  --mirror https://github.com/traefik/whoami \
  --ref master --port 80 --domain hello.example.com \
  --add-capabilities NET_BIND_SERVICE
```

`--mirror` points at an external Git repo the daemon clones and maintains a local mirror of; use `--source <path>` instead to push your own code directly into a bare repo the daemon creates for you. Either way there's no image registry involved — every user service is built locally on the Base from source at the pinned `ref:`. You can also skip the CLI and edit `ownbase.yaml` by hand (`ownbasectl config get`/`set`, or `git push` straight to the Base) — see [docs/ownbase-yaml.md](docs/ownbase-yaml.md) for the full schema.

`--add-capabilities` is only needed here because every container starts with every Linux capability dropped, and `whoami` listens directly on port 80 — a privileged port. Most images listen on an unprivileged port (3000, 8080, ...) by default and never need this flag at all.

```bash
ownbasectl status mybase   # confirm "hello" is running and healthy
```

### 4. Start the dev server and access the service

A fresh Base never opens ports 80/443, so there's no real TLS certificate to browse to yet — even before any domain's DNS is live, you can still see the service running, over trusted HTTPS, with:

```bash
ownbasectl dev mybase
```

```
ownbasectl: reading ownbase.yaml from "mybase" ...
ownbasectl: opening 1 SSH tunnel(s) to "mybase" ...
ownbasectl: generating local HTTPS certificate for 1 hostname(s) ...

Bridging:
  https://hello.example.com.localhost:8443

No code-sync — push to the service's bare repo and update ref: to deploy changes.
Press Ctrl+C to stop.
```

Open that URL — it works fully offline, needs no `/etc/hosts` entry, and stays the same across VM restarts. This is the one `ownbasectl` command allowed to prompt (a one-time `mkcert -install` to trust a local certificate authority). There's no code-sync: to iterate, push new code to the service's repo and bump `ref:` with `ownbasectl service update mybase hello --ref <branch>` — the daemon rebuilds and restarts it, and the dev bridge picks up the change automatically. Once a service's domain actually points DNS at the Base, it's reachable the same way in production, through Caddy.

### 5. Set up backups

Not optional — do this right after `create`, before you have data you'd miss:

```bash
ownbasectl backup setup mybase \
  --repo s3:s3.amazonaws.com/my-bucket/ownbase \
  --password <a-strong-restic-password> \
  --aws-access-key-id AKIA... --aws-secret-access-key ...
```

This runs the first snapshot immediately and schedules hourly snapshots plus a daily verified-restore drill by default (`--interval`/`--verify-interval` to change either). B2 and SFTP repos work too — see [docs/cli.md](docs/cli.md#backup-setuprunstatus-name). **Save the password somewhere durable — it is never recoverable from OwnBase.**

```bash
ownbasectl backup run mybase       # trigger an extra snapshot on demand
ownbasectl backup status mybase    # last snapshot, restorable?, last verify drill
```

### 6. Restore from backups

Whether the machine was lost or you tore it down yourself, rebuild onto a fresh VM or server with the same repo and password:

```bash
ownbasectl backup status mybase   # confirm "restorable: true" first — restore refuses an unverified snapshot without --force
ownbasectl restore mybase \
  --repo s3:s3.amazonaws.com/my-bucket/ownbase \
  --password <the-restic-password>
```

This provisions a fresh target (add `--remote <host>` for a fresh server), installs the daemon, restores the latest verified snapshot — the Base's own Git repo included, not just service data — and lets the daemon's normal reconcile bring every service back up.

### 7. Run a checkup

A single, plain-language health report — run it regularly (weekly is reasonable):

```bash
ownbasectl checkup mybase
```

It combines intrusion/access monitoring, network exposure, CVE scan results, per-service update drift, and backup health into one report, with the exact fix command next to each finding.

---

## Other things you can do

| Feature                  | Command                                                          | What it's for                                                                                                            |
| ------------------------ | ---------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| See what's deployed      | `ownbasectl status mybase`                                       | Services, security posture, recent daemon actions (`--json` for the full API payload)                                    |
| Track available updates  | `ownbasectl updates mybase`                                      | Per-service commits-behind and newest semver tag; you update by editing `ref:`                                           |
| Security posture & CVEs  | `ownbasectl security mybase`, `security scan`, `security fix`    | Exposure + SSH access report, on-demand CVE rescan, `apt-get upgrade` on the host                                        |
| Upgrade the core package | `ownbasectl upgrade mybase [--apply]`                            | Updates Caddy — the one package OwnBase manages outside `ownbase.yaml`                                                   |
| Manage secrets           | `ownbasectl secrets list\|get\|set\|delete mybase <service> ...` | Per-service secrets, age-encrypted on the Base, injected as env vars at container start                                  |
| Edit config as data      | `ownbasectl config get\|set mybase`                              | Read/replace the whole `ownbase.yaml` non-interactively — handy for scripts and agents                                   |
| Manage multiple Bases    | `ownbasectl list`, `ownbasectl adopt`, `ownbasectl delete`       | See all profiles/VMs, register a Base installed another way, tear one down                                               |
| Export everything        | see [docs/uninstall.md](docs/uninstall.md)                       | Clone every repo, export every volume, read out every secret — in standard formats                                       |
| Retire a Base            | see [docs/uninstall.md](docs/uninstall.md)                       | Remove OwnBase from the machine; the pass-zero hardening (UFW, fail2ban, updates) stays, so it's still a safe Ubuntu box |

---

## What's in this repo

The OwnBase source: the on-Base daemon and the CLI.

| Binary       | Purpose                                                                                                  |
| ------------ | -------------------------------------------------------------------------------------------------------- |
| `ownbased`   | The on-Base daemon. Reconciles, watches, backs up, and explains.                                         |
| `ownbasectl` | The CLI you install on your own machine: creates Bases, manages backups/secrets/updates, previews plans. |

## Documentation

| Doc                                                | What it covers                                                                    |
| -------------------------------------------------- | --------------------------------------------------------------------------------- |
| [MISSION.md](MISSION.md)                           | Why OwnBase exists, the promise, and the hard constraints                         |
| [INSTALL.md](INSTALL.md)                           | Setting up a Base end to end (VM or remote server), verifying a fresh install     |
| [docs/operating.md](docs/operating.md)             | The playbook for operating a running Base (human or AI)                           |
| [docs/cli.md](docs/cli.md)                         | Full `ownbasectl` command reference                                               |
| [docs/ownbase-yaml.md](docs/ownbase-yaml.md)       | The `ownbase.yaml` schema, the `ref:` update model, secrets, integrating services |
| [docs/api.md](docs/api.md)                         | The daemon's HTTP API: auth, every endpoint, request/response shapes              |
| [docs/troubleshooting.md](docs/troubleshooting.md) | When something fails: install, tunnel, tokens, Multipass, restic                  |
| [docs/uninstall.md](docs/uninstall.md)             | Retiring a Base: export your data, remove OwnBase, keep everything                |
| [docs/decisions.md](docs/decisions.md)             | Locked technical decisions: why the code is the way it is                         |
| [docs/development.md](docs/development.md)         | Building, testing, and the invariants to preserve when changing this code         |
| [docs/foundation/](docs/foundation/)               | The durable rules of how a Base works                                             |
| [AGENTS.md](AGENTS.md)                             | Dispatch for AI agents: which doc owns which job                                  |

---

## How a Base works

- **One config file.** `ownbase.yaml`, in a local, remote-less git repo on the Base itself, declares every service. Committing a change is the only mutation path: push (via `ownbasectl config`/`service`, or plain `git push` over SSH) → hook → reconcile → build → health-gated start. See [docs/ownbase-yaml.md](docs/ownbase-yaml.md).
- **No registries.** User services build locally from source at a pinned `ref:`. The core package (Caddy) is managed by `ownbasectl upgrade`.
- **Secrets stay home.** Per-service secrets are age-encrypted on the Base; the private key never leaves it. Managed with `ownbasectl secrets`, injected as env vars at container start.
- **Backups are verified.** Regular restic snapshots plus a periodic _verified restore drill_: `ownbasectl checkup` reports whether the Base is provably restorable, not just "backed up".
- **Everything is explained.** The config repo's seeded README tells any human or AI how to operate the Base safely; `ownbasectl status`/`checkup` (and the `/status` JSON API — [docs/api.md](docs/api.md)) report what is deployed, what is healthy, and what the security posture is, always current.

---

## Testing

```bash
go test ./...                    # Tier-1: runs anywhere, no VM
go test -tags=integration ./...  # Tier-2: requires the Ubuntu test VM
```

See [docs/development.md](docs/development.md) for VM setup and the full test workflow.

---

## Hard constraints

Six constraints govern every change to this project — user owns everything, nothing is mysterious, operations disappear, every service is ownable, boring technology wins, no pre-built application images. They do not change without a deliberate, explicit decision. The canonical statement lives in [MISSION.md](MISSION.md); the reasoning behind each lives in [docs/foundation/](docs/foundation/).

# OwnBase

> AI makes it easy to build software. OwnBase makes it easy to own it.

Grab a server or VM (cloud, home lab, anywhere) and OwnBase turns it into a **Base**: a hardened, self-updating home for your entire application layer. Everything above the OS is yours: services, databases, data, all running on hardware you control, with no third-party platform standing in between.

**Your server. Your software. No subscriptions.**

## Why you'd want this

- **You own everything.** The code, the server, the data, the config, the secrets, the backups, the domains. Nothing can be repriced, rate-limited, or shut down on you — and when you want to leave, you take all of it ([docs/uninstall.md](docs/uninstall.md)).
- **Your AI builds faster here.** Behind a platform API, your AI is a tenant; on a Base, it has the real machine. It can debug like an engineer, run any open-source software, stand up its own databases, and operate everything without guessing — the config repo's README carries the full operating contract, and one command returns the live status.
- **The sysadmin work disappears.** Firewall, intrusion protection, automatic security updates, TLS certificates, CVE scanning: set up during `create`, maintained by the daemon after. `ownbasectl checkup` tells you in plain language whether anything needs a glance.
- **Your data is provably safe.** Encrypted off-machine snapshots every hour, and a daily drill that _actually restores_ the latest backup and checks it. "Restorable" is a measured fact, not a checkbox.
- **One machine replaces a pile of subscriptions.** Auth, databases, job queues, and every app you build run together on one modest box — one predictable bill instead of a per-seat, per-usage sprawl.
- **No lock-in, structurally.** Every service is removable, forkable, and replaceable; config is plain files in a Git repo you own; uninstalling OwnBase leaves a working, still-hardened Ubuntu machine behind.

It all comes down to one idea:

```
reconstructable = (repo, secrets, backups)
```

Hold onto those three things and you can rebuild your entire Base from scratch on any new machine. Install, update, recover, and rebuild are the **same reconcile call** from different starting states; no state lives anywhere else.

---

## Quick start

```bash
# 1. Install the CLI (no Homebrew? see the manual install script in INSTALL.md)
brew install --cask ownbase-ai/tap/ownbasectl

# 2. Create a Base: give it a name; local VM by default, or a fresh Ubuntu server over SSH
ownbasectl create mybase
#    ...or: ownbasectl create mybase --remote root@mybase.example.com \
#             --caddy-email you@example.com

# 3. Turn on remote backups (runs the first snapshot immediately)
ownbasectl backup setup mybase --repo s3:s3.amazonaws.com/my-bucket/ownbase \
  --password <a-strong-restic-password> \
  --aws-access-key-id AKIA... --aws-secret-access-key ...

# 4. Health check: intrusions, exposure, CVEs, update drift, backup health
ownbasectl checkup mybase

# 5. If the machine is ever lost, rebuild it from backups
ownbasectl restore mybase --repo s3:... --password <the-restic-password>
```

`ownbasectl create` provisions the machine, hardens it (Podman, UFW, fail2ban, auto-updates), bootstraps **Caddy** and a local config repo, verifies the signed daemon binary, and registers the Base: one command, nothing to copy-paste.

Then declare your services: `ownbasectl service add mybase <name> --mirror <url> --ref main --port 3000`, or pull the config repo (`ownbasectl config get mybase`), add services to `ownbase.yaml` by hand, and push. The daemon builds them from source and brings them up health-gated. See [INSTALL.md](INSTALL.md) for the full walkthrough.

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

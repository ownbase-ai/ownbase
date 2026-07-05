# OwnBase

> The cost of creating software is collapsing.
> The cost of keeping it alive is not.
> OwnBase closes that gap.

Grab a server or VM (cloud, home lab, anywhere) and OwnBase turns it into a **Base**: a hardened, self-updating home for your entire application layer. Everything above the OS is yours: services, databases, data, all running on hardware you control, with no third-party platform standing in between. And because your AI has direct access to the real machine instead of a sandboxed API, it can debug like an engineer, run open-source software, and stand up its own databases, building faster than it ever could behind a platform.

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
#             --forgejo-domain git.yourdomain.com --caddy-email you@example.com

# 3. Turn on remote backups (runs the first snapshot immediately)
ownbasectl backup setup mybase --repo s3:s3.amazonaws.com/my-bucket/ownbase \
  --password <a-strong-restic-password> \
  --aws-access-key-id AKIA... --aws-secret-access-key ...

# 4. Health check: intrusions, exposure, CVEs, update drift, backup health
ownbasectl checkup mybase

# 5. If the machine is ever lost, rebuild it from backups
ownbasectl restore mybase --repo s3:... --password <the-restic-password>
```

`ownbasectl create` provisions the machine, hardens it (Podman, UFW, fail2ban, auto-updates), bootstraps **Forgejo** + **Caddy**, verifies the signed daemon binary, and registers the Base: one command, nothing to copy-paste.

Then declare your services: log into Forgejo (`ownbasectl forgejo mybase`), clone the config repo, add services to `ownbase.yaml`, and push. The daemon builds them from source and brings them up health-gated. See [INSTALL.md](INSTALL.md) for the full walkthrough.

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
| [INSTALL.md](INSTALL.md)                           | Setting up a Base end to end (VM or remote server), verifying a fresh install     |
| [docs/cli.md](docs/cli.md)                         | Full `ownbasectl` command reference                                               |
| [docs/ownbase-yaml.md](docs/ownbase-yaml.md)       | The `ownbase.yaml` schema, the `ref:` update model, secrets, integrating services |
| [docs/api.md](docs/api.md)                         | The daemon's HTTP API: auth, every endpoint, request/response shapes              |
| [docs/troubleshooting.md](docs/troubleshooting.md) | When something fails: install, tunnel, tokens, Multipass, restic                  |
| [docs/uninstall.md](docs/uninstall.md)             | Retiring a Base: export your data, remove OwnBase, keep everything                |
| [docs/decisions.md](docs/decisions.md)             | Locked technical decisions: why the code is the way it is                         |
| [docs/foundation/](docs/foundation/)               | The durable rules of how a Base works                                             |
| [AGENTS.md](AGENTS.md)                             | Guide for AI agents: operating a Base, or modifying this codebase                 |

---

## How a Base works

- **One config file.** `ownbase.yaml`, in a git repo on the Base's own Forgejo, declares every service. Committing a change is the only mutation path: push → hook → reconcile → build → health-gated start. See [docs/ownbase-yaml.md](docs/ownbase-yaml.md).
- **No registries.** User services build locally from source at a pinned `ref:`. Core packages (Forgejo, Caddy) are managed by `ownbasectl upgrade`.
- **Secrets stay home.** Per-service secrets are age-encrypted on the Base; the private key never leaves it. Managed with `ownbasectl secrets`, injected as env vars at container start.
- **Backups are verified.** Regular restic snapshots plus a periodic _verified restore drill_: `ownbasectl checkup` reports whether the Base is provably restorable, not just "backed up".
- **Everything is explained.** After every reconcile the daemon rewrites `OWNBASE.md` on the Base: a briefing any human or AI can read to understand what is deployed, what is healthy, and what the security posture is. The same data is served as JSON at `/status` ([docs/api.md](docs/api.md)).

---

## Testing

```bash
go test ./...                    # Tier-1: runs anywhere, no VM
go test -tags=integration ./...  # Tier-2: requires the Ubuntu test VM
```

See [AGENTS.md](AGENTS.md) for VM setup and the full test workflow.

---

## Hard constraints

These do not change without a deliberate, explicit decision (see [docs/foundation/](docs/foundation/)):

| Constraint                      | Detail                                                                         |
| ------------------------------- | ------------------------------------------------------------------------------ |
| User owns everything            | Code, server, data, config, secrets, backups, domains                          |
| Nothing is mysterious           | Plain files, Git as source of truth, human- and AI-readable layouts            |
| Operations disappear            | If the user must learn Docker, systemd, or nginx because of OwnBase, we failed |
| Every service is ownable        | Removable, forkable, replaceable, data accessible, works standalone            |
| Boring technology wins          | Ubuntu, Podman, Postgres, Git, Caddy; never Kubernetes                         |
| No pre-built application images | Every service is built locally from source at `ref:`                           |

# `ownbasectl` command reference

> The complete CLI surface. Every command also has `--help`; this page adds context the terse help text can't.

## Installing

```bash
brew install --cask ownbase-ai/tap/ownbasectl
```

Or download the archive for your platform from [GitHub Releases](https://github.com/ownbase-ai/ownbase/releases) and put `ownbasectl` on your `PATH`. Verify with `ownbasectl version`.

## Design

Every command that targets a Base takes its name as a required first argument — there is no `--server`/`--vm` flag and no default Base to fall back to:

```bash
ownbasectl status mybase
ownbasectl secrets list mybase
```

`--help`, `-h`, and `--version` work everywhere. Shell completions: `ownbasectl completion bash|zsh|fish|powershell` (see `ownbasectl completion --help` for install instructions per shell).

## How commands reach a Base

Commands that talk to a Base (`status`, `updates`, `security`, `secrets`, `forgejo`, `upgrade`, `backup`, `checkup`) open an SSH tunnel to the host in the named profile (`~/.ownbase/config`) and call the daemon's HTTP API through it (see [api.md](api.md)). The API port is never exposed to the network. Host keys are verified against `~/.ownbase/known_hosts` (trust-on-first-use, like the `ssh` CLI).

---

## Lifecycle: create, adopt, list, delete, restore

### `create <name> [--remote <ssh-host>]`

Provision a Base end to end and register it in `~/.ownbase/config`.

```bash
# Local Multipass VM (the default when --remote is omitted) — gets real
# HTTPS by default, see "dev-TLS" below
ownbasectl create mybase

# Local VM, plain HTTP on :3000 (today's pre-dev-TLS behavior)
ownbasectl create mybase --no-dev-tls

# Fresh remote Ubuntu 22.04/24.04 server
ownbasectl create mybase --remote root@mybase.example.com \
  --forgejo-domain git.yourdomain.com --caddy-email you@example.com
```

| Flag | Default | Meaning |
|---|---|---|
| `--remote <host>` | — | SSH host of a fresh Ubuntu server; accepts `user@host` (omit for a local VM) |
| `--ssh-user` | `root` | SSH login user for `--remote` (ignored for a VM) |
| `--ssh-key` | `~/.ssh/id_ed25519` | SSH private key for `--remote` |
| `--ssh-port` | `22` | SSH port for `--remote` (persisted in the profile) |
| `--cpus` / `--memory` / `--disk` | `2` / `2` GB / `15` GB | VM sizing (local VM only) |
| `--forgejo-domain` | — | Public domain for the Forgejo UI (optional; without it a local VM defaults to dev-TLS — see below — and a remote server serves plain `http://<host>:3000`); implies `--no-dev-tls` |
| `--caddy-email` | — | ACME contact email for automatic TLS (used with `--forgejo-domain`); implies `--no-dev-tls` |
| `--no-dev-tls` | `false` | Disable local HTTPS simulation (mkcert + `/etc/hosts`) for a local VM; no-op for `--remote` |
| `--dev-domain` | `<name>.test` | Base domain for dev-TLS (local VM only; e.g. `mybase.test` → `forgejo.mybase.test`) |
| `--yes`, `-y` | `false` | Skip confirmation prompts (e.g. overwriting an existing local VM) |

If a local VM with the same name already exists, `create` asks before deleting it (`--yes` skips the prompt; non-interactive runs proceed as before).

### `dev-tls` and `vm` (local VM only)

A local VM created without `--no-dev-tls`/`--forgejo-domain`/`--caddy-email` gets real HTTPS: `create` runs `mkcert -install` (trusts a local-only CA in the host's system/browser trust stores, once per machine), generates a wildcard certificate for `*.<name>.test`, transfers it into the VM, and Caddy serves it — no ACME, no public DNS, no rate limits. `/etc/hosts` gets a marked block so `https://forgejo.mybase.test` resolves to the VM's IP.

```bash
ownbasectl dev-tls sync <name>    # refresh /etc/hosts after adding a service with a new domain:
ownbasectl dev-tls trust          # re-run mkcert -install (new machine, or after mkcert -uninstall)

ownbasectl vm start <name>        # start a stopped local VM; re-detects its IP, updates the
                                   # profile, and refreshes /etc/hosts for dev-TLS Bases
ownbasectl vm stop <name>         # stop a local VM (data is preserved; IP will change on next start)
ownbasectl vm restart <name>      # stop + start, same refresh as above
ownbasectl vm list                # local-VM Bases and their current Multipass state
ownbasectl vm ip <name>           # current Multipass IPv4; warns if it differs from the saved profile
```

`vm start`/`restart` replace the old manual `ownbasectl adopt --host <new-ip>` dance needed after every Multipass `stop`/`start` (the VM's DHCP-assigned IP very likely changes). If `mkcert` isn't installed on the host, `create` prints a warning with an install hint (`brew install mkcert nss`) and falls back to plain HTTP for that run — dev-TLS is best-effort and never blocks provisioning.

### `adopt <name> --host <host> --token <token>`

Register a Base that was installed some other way (e.g. `install.sh` run by hand). Verifies SSH connectivity before saving. Bases created with `create` are registered automatically — `adopt` is only needed for an already-installed Base.

```bash
ownbasectl adopt prod --host mybase.example.com --token <token>
```

Flags: `--host` (required), `--token` (required — printed at install time, stored at `/opt/ownbase/api-token` on the Base), `--ssh-user` (default `root`; local VMs use `ubuntu`), `--ssh-key` (default `~/.ssh/id_ed25519`), `--ssh-port` (default `22`), `--api-port` (default `7070`).

### `list` / `delete <name>`

```bash
ownbasectl list                       # profiles + local VMs, unregistered VMs flagged
ownbasectl delete mybase              # destroy the local VM (if any) + remove the profile; asks y/N
ownbasectl delete mybase --keep-vm    # remove only the profile
ownbasectl delete mybase --yes        # skip the confirmation prompt
```

`delete` never destroys a remote server — for a profile known to be remote it only removes the local profile.

### `restore <name> --repo <restic-url> --password <pw> [--remote <host>]`

Reconstruct a Base from backups onto a fresh VM or server — the disaster-recovery drill as one command.

```bash
ownbasectl restore mybase \
  --repo s3:s3.amazonaws.com/my-bucket/ownbase \
  --password <the-restic-password>
```

Takes all the provisioning flags of `create`, plus the credential flags of `backup setup`, plus:

| Flag | Meaning |
|---|---|
| `--repo` | restic repository URL to restore from (required; same flag as `backup setup`) |
| `--force` | restore even if the latest snapshot was never verified restorable |

---

## Health and backups

### `checkup <name>`

One aggregated health report: intrusion/access monitoring, network exposure, CVE scan results, service update drift, and backup health — each finding paired with the exact command that fixes it. Run it regularly (weekly is reasonable). `--json` prints the raw status payload.

### `backup setup|run|status <name>`

```bash
ownbasectl backup setup mybase --repo s3:s3.amazonaws.com/my-bucket/ownbase \
  --password <a-strong-restic-password> \
  --aws-access-key-id AKIA... --aws-secret-access-key ...

ownbasectl backup run mybase       # trigger an immediate snapshot ("save now")
ownbasectl backup status mybase    # last snapshot, restorable?, last verify drill (--json for raw)
```

`setup` is lifecycle step 2 — right after `create` — for local VMs and remote servers alike.

| Flag (setup) | Meaning |
|---|---|
| `--repo` | restic repository URL — `s3:`, `b2:`, or `sftp:` (required) |
| `--password` | restic repository encryption password (required; **save it — it is never recoverable from OwnBase**) |
| `--aws-access-key-id` / `--aws-secret-access-key` | credentials for `s3:` repos |
| `--b2-account-id` / `--b2-account-key` | credentials for `b2:` repos |
| `--interval` | snapshot cadence (default `1h`) |
| `--verify-interval` | verified-restore drill cadence (default `24h`) |

Credentials are stored age-encrypted on the Base; the repo URL and cadence are committed to `ownbase.yaml` through the daemon's API. No daemon restart needed.

---

## Observability commands

### `status <name>`

Summary of services, security posture, and recent daemon actions.

```bash
ownbasectl status mybase              # formatted summary
ownbasectl status mybase --json       # full BaseStatus JSON (schema v3 — see api.md)
```

### `updates <name>`

Per-service drift table: pinned `ref:`, commits behind the default branch, newest semver tag. Updates are user-driven — edit `ref:` in `ownbase.yaml` and commit.

```bash
ownbasectl updates mybase
ownbasectl updates mybase --json      # only the "updates" section of the status payload
```

### `security <name>` / `security scan <name>` / `security fix <name>`

```bash
ownbasectl security mybase            # exposure + SSH access + CVE report
ownbasectl security mybase --json     # only the "security" section of the status payload
ownbasectl security scan mybase       # trigger an immediate CVE rescan (~2–5 min)
ownbasectl security fix mybase        # apt-get upgrade on the Base; prints a notice, then streams output
```

Fixing CVEs by location:

| Location | Command | What it does |
|---|---|---|
| Host OS packages | `ownbasectl security fix <name>` | `apt-get upgrade` on the Base; auto-rescans after |
| Forgejo / Caddy images | `ownbasectl upgrade <name> --apply` | Pulls latest pinned image, restarts container; auto-rescans after |
| Image CVE with no fix | — | Wait for the upstream maintainer to release an updated image |

### `upgrade <name>`

Check or apply updates to the OwnBase core packages (Forgejo, Caddy). Core packages are managed by OwnBase — not by `ownbase.yaml` — and this command is the only supported way to update them.

```bash
ownbasectl upgrade mybase             # check: image + digest + running state per core package
ownbasectl upgrade mybase --apply     # pull latest pinned images, restart core containers (streams progress)
```

---

## Secrets and Forgejo

### `secrets list|get|set|delete <name> ...`

Per-service secrets, age-encrypted on the Base, injected into the service's container as environment variables at start.

```bash
ownbasectl secrets list mybase                  # services that have secrets
ownbasectl secrets list mybase myapp            # key names for one service
ownbasectl secrets get  mybase myapp DB_URL     # value; no trailing newline when piped
ownbasectl secrets set  mybase myapp DB_URL=postgres://... API_KEY=abc
ownbasectl secrets delete mybase myapp DB_URL
```

Plaintext travels only inside the SSH tunnel; the age private key never leaves the Base.

### `forgejo <name>`

Print the Forgejo admin username and password (from `/opt/ownbase/forgejo-admin-pass` on the Base). Useful after a fresh install to log into the Forgejo web UI.

---

## Local commands (no Base connection)

These operate on a checkout of a Base config repo and are mostly used for development and previews. They take no Base name.

### `compile --dir <path>`

Compile `ownbase.yaml` into runtime files (Quadlet units, Caddyfile, docker-compose.yml) under `runtime/`.

### `plan --dir <path>`

Show what would change: the diff between the compiled desired state and what is currently running.

```
+ start  ownbase-auth
+ start  ownbase-crm
  skip   ownbase-postgres  (already running)
```

### `apply --dir <path> [--dry-run]`

Apply the plan. `--dry-run` previews with no side effects; a real apply requires Ubuntu + Podman (it is what the daemon runs on the Base).

### `version`

Print the version, commit, and build date (release builds) or `dev (built from source)`.

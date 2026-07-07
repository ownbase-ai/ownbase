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

Commands that talk to a Base (`status`, `updates`, `security`, `secrets`, `config`, `service`, `upgrade`, `backup`, `checkup`) open an SSH tunnel to the host in the named profile (`~/.ownbase/config`) and call the daemon's HTTP API through it (see [api.md](api.md)). The API port is never exposed to the network. Host keys are verified against `~/.ownbase/known_hosts` (trust-on-first-use, like the `ssh` CLI).

---

## Lifecycle: create, adopt, list, delete, restore

### `create <name> [--remote <ssh-host>]`

Provision a Base end to end and register it in `~/.ownbase/config`.

```bash
# Local Multipass VM (the default when --remote is omitted)
ownbasectl create mybase

# Fresh remote Ubuntu 22.04/24.04 server
ownbasectl create mybase --remote root@mybase.example.com \
  --caddy-email you@example.com
```

| Flag | Default | Meaning |
|---|---|---|
| `--remote <host>` | — | SSH host of a fresh Ubuntu server; accepts `user@host` (omit for a local VM) |
| `--ssh-user` | `root` | SSH login user for `--remote` (ignored for a VM) |
| `--ssh-key` | `~/.ssh/id_ed25519` | SSH private key for `--remote` |
| `--ssh-port` | `22` | SSH port for `--remote` (persisted in the profile) |
| `--cpus` / `--memory` / `--disk` | `2` / `2` GB / `15` GB | VM sizing (local VM only) |
| `--caddy-email` | — | ACME contact email for automatic TLS on public domains |
| `--yes`, `-y` | `false` | Skip confirmation prompts (e.g. overwriting an existing local VM) |

If a local VM with the same name already exists, `create` asks before deleting it (`--yes` skips the prompt; non-interactive runs proceed as before). Every other step of `create` (and of `vm start|stop|restart`) is guaranteed to never prompt for anything, ever — this is what makes it safe for an AI agent to run unattended. A freshly created Base has no domain configured anywhere, so it exposes nothing but SSH externally (Caddy publishes no ports, the firewall opens no web ports); once a service has a `domain:` (or `domains:`), reach it locally over trusted HTTPS with `ownbasectl dev` (below), or reach it in production once its domain's DNS points at the Base.

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
| Caddy image | `ownbasectl upgrade <name> --apply` | Pulls latest pinned image, restarts container; auto-rescans after |
| Image CVE with no fix | — | Wait for the upstream maintainer to release an updated image |

### `upgrade <name>`

Check or apply updates to the OwnBase core package (Caddy). The core package is managed by OwnBase — not by `ownbase.yaml` — and this command is the only supported way to update it.

```bash
ownbasectl upgrade mybase             # check: image + digest + running state
ownbasectl upgrade mybase --apply     # pull latest pinned image, restart the container (streams progress)
```

---

## Config and services

### `config get|set <name>`

Read or atomically replace `ownbase.yaml` — the agent-first way to script config changes without an editor.

```bash
ownbasectl config get mybase                       # print the current ownbase.yaml
ownbasectl config get mybase --json                # same, decoded to JSON

ownbasectl config set mybase --file ./ownbase.yaml # validate locally, then push
cat ownbase.yaml | ownbasectl config set mybase    # or read from stdin
```

`set` validates the whole document locally before sending it, then pushes it through the daemon's front-door `/config` endpoint — the same commit path a manual `git push` to `/opt/ownbase/repo` takes. Exit code is non-zero on validation failure or transport error, so this is safe to call unattended from a script or an AI agent.

### `service add|remove|update <name> <service> ...`

Structured, non-interactive edits to the `services:` map — a thin, scriptable layer over `config get`/`config set`.

```bash
ownbasectl service add mybase crm --mirror https://github.com/org/crm --ref main --port 3000 --domain crm.example.com
ownbasectl service update mybase crm --ref v2.3.0        # bump the pinned ref
ownbasectl service update mybase crm --port 4000 --domain crm.example.com
ownbasectl service update mybase crm --domains crm.example.com,crm.example.org  # serve two hostnames
ownbasectl service remove mybase crm
```

`add` requires exactly one of `--source`/`--mirror`. `update` only touches the fields whose flags were explicitly passed — every other field of the service keeps its current value. `--env` merges into the existing list (new values win on a duplicate key); `--requires` and `--domains` replace their respective lists entirely when passed. `--domain` (singular) still works and is combined with `--domains`, deduplicated. All three subcommands accept `--json` for structured output.

---

## Local development: `dev <name>`

The one command in `ownbasectl` that is allowed to prompt interactively — starting it is itself a human's explicit "I am sitting here, ready to develop" signal (see [decisions.md](decisions.md)). `create`/`vm` never prompt for anything; this command is the only exception, and only for a one-time `mkcert -install` (trusting a local certificate authority in this machine's OS/browser trust store).

```bash
ownbasectl dev mybase
ownbasectl dev mybase --port 9443   # override the local bind port (default 8443)
```

It reads the Base's live `ownbase.yaml` over SSH, opens one SSH tunnel per service that has both a `port:` and a domain configured (`domain:` or `domains:`) directly to that service's dedicated loopback port — bypassing Caddy entirely, so no port is firewalled on the Base — and serves each at its real domain with `.localhost` appended, e.g. a service with `domain: myapp.example.com` is served at `https://myapp.example.com.localhost:8443`. Per RFC 6761 any hostname ending in `.localhost` always resolves to loopback, with no `/etc/hosts` entry and no DNS lookup, so the URL never changes across a `vm restart` or IP change. A service with **no** domain configured is never bridged — not tunneled, not exposed, not printed.

Each bridged service's loopback port is deliberately a different number than its own `port:` — assigned deterministically starting at 41000 by sorted service name (`schema.OwnbaseConfig.DevBridgePorts()`) — so a service can declare `port: 80`/`443` without colliding with Caddy's own machine-wide bind, and two services can share the same `port:` without colliding with each other. `dev` computes this the same way the daemon's compiler does, straight from `ownbase.yaml`, with no daemon call needed to agree on the number.

```
ownbasectl: reading ownbase.yaml from "mybase" ...
ownbasectl: opening 1 SSH tunnel(s) to "mybase" ...
ownbasectl: generating local HTTPS certificate for 1 hostname(s) ...

Bridging:
  https://myapp.example.com.localhost:8443

No code-sync — push to the service's bare repo and update ref: to deploy changes.
Press Ctrl+C to stop.
```

**There is no code-sync mechanism.** `ownbasectl dev` only tunnels and proxies traffic to whatever is currently deployed — no bind mount, file watcher, or hot-reload. To iterate on a service's code, use the same git-push-to-deploy flow as production:

```bash
git push ssh://<ssh-user>@<host>/opt/ownbase/repos/<service> my-branch:my-branch
ownbasectl service update mybase <service> --ref my-branch
```

The daemon fetches/builds/restarts the service exactly as it would for any other `ref:` change; the dev bridge, if still running, picks up the new container transparently since it tunnels to the service's port, not to a specific container instance.

---

## Secrets

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

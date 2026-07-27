# `ownbasectl` command reference

> The complete CLI surface: what each command does, its flags, and its defaults. Every command also has `--help`. For *why* the CLI is shaped this way see [decisions.md](decisions.md); for setting up a Base see [README.md](../README.md#setting-up-a-base).

## Design

Every command that targets a Base takes its name as a required first argument. There is no `--server` flag and no default Base:

```bash
ownbasectl status mybase
ownbasectl secrets list mybase
```

`--help`, `-h`, and `--version` work everywhere. Shell completions: `ownbasectl completion bash|zsh|fish|powershell`.

**Only `tunnel` is ever interactive.** Everything else runs unattended, which is what makes the CLI safe for an AI agent to drive. `create`, `restore`, `delete`, and `db restore --into production` have confirmation prompts that apply solely to destructive local-VM or production operations, and each is skipped by `--yes` or by a non-TTY stdin.

## How commands reach a Base

Commands that talk to a Base open an SSH tunnel to the host in the named profile (`~/.ownbase/config`) and call the daemon's HTTP API through it ([api.md](api.md)). The API port is never exposed to the network. Host keys are verified against `~/.ownbase/known_hosts`, trust-on-first-use, like the `ssh` CLI.

Mutating config commands additionally clone and push the external config repo directly from your machine with your own git credentials. The Base only ever reads that repo.

## Exit codes

`create` and `restore` classify failures so an unattended caller can react to them:

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Unclassified failure |
| 2 | Bad flags or arguments |
| 3 | Preflight failed — target unreachable or unfit. Nothing was changed |
| 4 | The installer ran and failed |
| 5 | Installed, but not healthy within `--wait-timeout` |

Every other command uses 0 for success and 1 for failure.

## stdout and stderr

Stdout carries only the result: the human banner, or the `--json` document. Progress lines, installer logs, and errors go to stderr on every path, local VM and remote alike. So `ownbasectl create ... --json | jq` is always safe, and dropping stderr is what makes a run quiet rather than what makes it parseable.

---

## Lifecycle

### `keygen <name>`

Create the SSH keypair **you** use to reach a Base, at `~/.ssh/ownbase_<name>`, and print the public half to paste into a provider's SSH key field. Run it before `create --remote`, because providers authorize a key when the machine is created.

```bash
ownbasectl keygen mybase
ownbasectl keygen mybase --json    # {"public_key", "private_key_path", "created"}
```

Idempotent: an existing key is printed, never regenerated. `create` finds the key automatically, so `--ssh-key` is not needed.

This is not the same as [`ssh-key`](#ssh-key-addlist-base), which provisions the key the *Base* uses to clone your git repos.

### `create <name> [--remote <ssh-host>]`

Provision a Base end to end and register it in `~/.ownbase/config`.

```bash
ownbasectl create mybase                                         # local Multipass VM
ownbasectl create mybase --remote root@203.0.113.10 --wait       # fresh Ubuntu server
```

| Flag | Default | Meaning |
|---|---|---|
| `--remote <host>` | — | SSH host of a fresh Ubuntu server; accepts `user@host`. Omit for a local VM |
| `--wait` | `false` | Block until the daemon reports healthy, meaning host hardening has finished |
| `--json` | `false` | Machine-readable result instead of the banner |
| `--caddy-email` | — | ACME contact for automatic TLS on public domains |
| `--ssh-user` | `root` | SSH login user for `--remote`. Needs passwordless sudo if not root |
| `--ssh-key` | the `keygen` key for this Base, else `~/.ssh/id_ed25519` | SSH private key |
| `--ssh-port` | `22` | SSH port. Also tells the daemon which port to open in UFW and jail in fail2ban |
| `--wait-for-ssh` | `5m` | How long to wait for a booting server to accept SSH |
| `--wait-timeout` | `10m` | How long `--wait` blocks before giving up |
| `--replace` | `false` | Allow an existing Base name to be repointed at a different machine |
| `--cpus` / `--memory` / `--disk` | `2` / `2` GB / `15` GB | VM sizing, local VM only |
| `--yes`, `-y` | `false` | Skip the prompt before deleting an existing local VM |

On the `--remote` path, `create` waits for SSH, then verifies passwordless sudo, the Ubuntu version, the architecture, and that the machine meets the memory and disk floor — all **before** it changes anything. See [INSTALL.md](../INSTALL.md#installing-on-a-server).

The key `create` will connect with is resolved up front. For a local VM a missing key is generated, so `create <name>` works with no prior setup. For `--remote` a missing key is a preflight failure: the provider has to authorize it before the server boots, so run [`keygen`](#keygen-name) first.

Without `--wait`, `create` returns while the daemon is still hardening the host for another minute or two. The daemon runs pass zero before it binds its API port, so "the API answers" is exactly the signal that hardening finished.

`create` refuses to repoint an existing Base name at a different machine without `--replace`, because overwriting the profile discards the old Base's API token and orphans it. This applies to both paths: repointing a name at another host, and launching a local VM under a name that already belongs to a remote server.

"Same machine" means the address as written, normalized for case, whitespace, a trailing dot, and IPv6 spelling. DNS is not resolved, so reaching one server by hostname and then by IP reads as two machines — see [troubleshooting.md](troubleshooting.md#already-points-at-host-in-ownbaseconfig) for why that is deliberate, and why `--replace` is the right answer there.

A freshly created Base has no domain configured, so it exposes nothing but SSH. Once a service has a `domain:`, reach it with [`tunnel`](#tunnel-name), or through Caddy once DNS points at the Base.

### `adopt <name> --host <host> --token <token>`

Register a Base installed some other way, e.g. `install.sh` run by hand. Verifies SSH connectivity before saving. Bases made with `create` are registered automatically.

```bash
ownbasectl adopt prod --host mybase.example.com --token <token>
```

Flags: `--host` and `--token` (required; the token is printed at install time and stored at `/opt/ownbase/api-token`), `--ssh-user` (default `root`; local VMs use `ubuntu`), `--ssh-key` (default `~/.ssh/id_ed25519`), `--ssh-port` (default `22`), `--api-port` (default `7070`).

### `list` / `delete <name>`

```bash
ownbasectl list                       # profiles + local VMs, unregistered VMs flagged
ownbasectl delete mybase              # destroy the local VM (if any) + remove the profile; asks y/N
ownbasectl delete mybase --keep-vm    # remove only the profile
ownbasectl delete mybase --yes        # skip the confirmation
```

`delete` never destroys a remote server. For a profile known to be remote it removes only the local profile.

### `restore <name> --repo <restic-url> --password <pw>`

Reconstruct a Base from backups onto a fresh VM or server — the disaster-recovery drill as one command.

```bash
ownbasectl restore mybase \
  --repo s3:s3.amazonaws.com/my-bucket/ownbase \
  --password <the-restic-password>
```

Takes every provisioning flag of `create` plus the credential flags of `backup setup`, plus:

| Flag | Meaning |
|---|---|
| `--repo` | restic repository URL to restore from (required) |
| `--force` | restore even if the latest snapshot was never verified restorable |

Restore expects to point the Base's name at a new machine, so `--replace` is not needed. This is also how you move to a bigger server.

---

## Health and backups

### `checkup <name> [--verify]`

One aggregated health report — intrusion and access monitoring, network exposure, CVE results, service update drift, backup health — each finding paired with the command that fixes it. Weekly is a reasonable cadence.

`--verify` runs the verified-restore drill first: the Base restores its newest snapshot into an isolated directory, checks it, and when Postgres is in the backup starts a real database from it and waits for recovery. That takes minutes, so progress streams:

```
Verified-restore drill
────────────────────────────────────────────────────────────────────
==> Restoring snapshot 4f2a91c into an isolated directory
==> Running integrity checks
    ✓ restic-check                 repository integrity OK
    ✓ postgres-recovery            recovered to 2026-07-25 18:04:11+00, 214 relations

  ✓ Restore verified — every check passed.
```

Without `--verify`, the report shows the last drill the Base ran on its own schedule, which may be up to a day old. A failed drill names the failing check and exits non-zero; the report still prints.

`--json` is one document either way: the `/status` payload on its own, or `{"verify": {…}, "status": {…}}` with `--verify`. A failed drill still exits non-zero with its message on stderr, so stdout stays parseable.

### `backup setup|run|status <name>`

```bash
ownbasectl backup setup mybase --repo s3:s3.amazonaws.com/my-bucket/ownbase \
  --password <a-strong-password> \
  --aws-access-key-id AKIA... --aws-secret-access-key ...

ownbasectl backup run mybase       # trigger an immediate snapshot
ownbasectl backup status mybase    # last snapshot, restorable?, last verify drill
```

| Flag (setup) | Meaning |
|---|---|
| `--repo` | restic repository URL — `s3:`, `b2:`, or `sftp:` (required) |
| `--password` | repository encryption password (required; **save it — it is never recoverable from OwnBase**) |
| `--aws-access-key-id` / `--aws-secret-access-key` | credentials for `s3:` repos |
| `--b2-account-id` / `--b2-account-key` | credentials for `b2:` repos |
| `--interval` | snapshot cadence (default `1h`) |
| `--verify-interval` | verified-restore drill cadence (default `24h`) |

Credentials are stored age-encrypted on the Base; the repo URL and cadence are committed to `ownbase.yaml` client-side and applied by a reconcile, with no daemon restart.

`backup status --json` prints the **full** `/status` payload, not just the backup section.

### `db status <name>`

`backup status` answers whether the restic snapshot is good. `db status` answers the question underneath: how far back this Postgres can be recovered, and whether that window is still moving.

```bash
ownbasectl db status mybase
ownbasectl db status mybase --json    # raw GET /db/status
```

```
─────────────────────── Postgres Recovery Status ───────────────────────
  Stanza:        main  (PostgreSQL 17)
  Repository:    ✓ ok
  Backups held:  3

  Recovery window: Jul 24 02:07:11  →  Jul 25 18:04:11
  WAL archive:     000000010000000000000002 → 00000001000000000000001F

  Archiving:     ✓ 74 segments, last Jul 25 18:04:11
────────────────────────────────────────────────────────────────────────
```

The line to watch is `Archiving`. When it fails, nothing else about the Base looks wrong while the recovery window quietly stops moving. See [troubleshooting.md](troubleshooting.md#postgres-point-in-time-recovery) for what that means and how to fix it.

### `db restore <name> [--to <timestamp>] [--into scratch|production]`

Point-in-time recovery from the pgBackRest repository. Streams progress, because a restore takes minutes and its failure modes happen mid-flight.

```bash
# Look at yesterday's data. Production keeps serving.
ownbasectl db restore mybase --to "2026-07-25 14:00:00+00"

# Take production back to just before a bad migration.
ownbasectl db restore mybase --to "2026-07-25 14:00:00+00" --into production
```

| Flag | Meaning |
|---|---|
| `--to` | recovery target, e.g. `"2026-07-25 14:00:00+00"`. No zone means UTC. Omit to recover everything the repository holds |
| `--into` | `scratch` (default) or `production` |
| `--scratch-port` | loopback port for the scratch instance (default `5433`) |
| `-y`, `--yes` | skip the confirmation prompt for `--into production` |
| `--json` | suppress the progress stream and print only the final result |

`--into scratch` brings up a second Postgres on `127.0.0.1:5433` and leaves it running to be inspected; production is untouched. This is how a recovery should normally start. Teardown is `podman rm -f ownbase-db-scratch`, and a second restore replaces it rather than accumulating instances.

`--into production` stops the database and every service that `requires:` it, restores over the live data directory, replays the archive, and then **takes a full backup** — not optional, since a promotion starts a new timeline that no existing backup is on.

`--to` is validated against the repository before anything is stopped. Asking for "right now" usually fails, for a reason worth understanding: see [troubleshooting.md](troubleshooting.md#postgres-point-in-time-recovery).

---

## Observability

### `status <name>`

Services, security posture, and recent daemon actions.

```bash
ownbasectl status mybase
ownbasectl status mybase --json       # full BaseStatus JSON (see api.md)
```

### `updates <name>`

Per-service drift: pinned `ref:`, commits behind the default branch, newest semver tag. Move a service with [`deploy`](#deploy-name-service---ref-shatagbranch).

```bash
ownbasectl updates mybase
ownbasectl updates mybase --json      # only the "updates" section
```

### `security <name>` / `security scan` / `security fix`

```bash
ownbasectl security mybase            # exposure + SSH access + CVE report
ownbasectl security mybase --json     # only the "security" section
ownbasectl security scan mybase       # immediate CVE rescan (~2–5 min)
ownbasectl security fix mybase        # apt-get upgrade on the Base, streamed
```

| CVE location | Command |
|---|---|
| Host OS packages | `ownbasectl security fix <name>` — auto-rescans after |
| Caddy image | `ownbasectl upgrade <name> --apply` — auto-rescans after |
| Image CVE with no fix available | Wait for the upstream maintainer |

### `upgrade <name>`

Check or apply updates to the OwnBase core package (Caddy) — the one package managed outside `ownbase.yaml`.

```bash
ownbasectl upgrade mybase             # check: image, digest, running state
ownbasectl upgrade mybase --apply     # pull and restart, streaming progress
```

---

## Config repo, deploy, and services

`ownbase.yaml` lives in an **external git repo you own**. Mutating commands (`config set`, `service *`, `deploy`, `backup setup`) run client-side: `ownbasectl` clones the repo, edits, commits, and pushes with **your** credentials, then asks the Base to pull and reconcile. The Base needs only **read** access.

### `ssh-key add|list <base>`

Provision the Base's read-only git deploy identity — the key the *Base* uses to clone your repos, as distinct from the [`keygen`](#keygen-name) key you use to reach the Base.

`add` generates an ed25519 key under `/opt/ownbase/ssh` if none exists, records the given host's SSH host keys, and prints the public key. Register it as a **read-only deploy key** on the config repo and every service repo.

```bash
ownbasectl ssh-key add mybase --host github.com
ownbasectl ssh-key list mybase
```

Both accept `--json`, which emits `{"public_key": "..."}`.

### `config setup <name> --repo <url> [--ref <branch>] [--init]`

Point the Base at its config repo. Persists the URL and ref to the local profile, then tells the Base to clone read-only and reconcile. `--ref` defaults to `main`.

```bash
ownbasectl config setup mybase --repo git@github.com:org/mybase-config.git --init
```

`--init` seeds an **empty existing** remote with a default `ownbase.yaml`. It never creates the remote itself.

That seed is a working **Postgres 17 with point-in-time recovery** — a `postgres` service plus the `pgbackrest` repository host that owns its WAL archive — rather than an empty `services:` map. Almost every Base needs a database, and the settings that make one recoverable (the AppArmor exception Postgres will not start without, the capabilities `sshd` needs, backing up the pgBackRest repository rather than the live data directory) are exactly the ones nobody discovers unaided. Each is commented in the seeded file with what breaks if removed. The SSH keypair and Postgres password are [`generated_secrets:`](ownbase-yaml.md#generated-secrets-generated_secrets), created by the Base on its first reconcile. Delete both services if this Base needs no database.

### `config get|set <name>`

```bash
ownbasectl config get mybase                       # current ownbase.yaml, from the Base's checkout
ownbasectl config get mybase --json                # same, decoded to JSON

ownbasectl config set mybase --file ./ownbase.yaml # validate, commit, push
cat ownbase.yaml | ownbasectl config set mybase    # or from stdin
ownbasectl config set mybase --file x.yaml --message "add worker"
```

`set` validates the whole document locally before committing. Non-zero exit on validation failure or transport error, so it is safe to call from a script.

### `deploy <name> <service> [--ref <sha|tag|branch>]`

The single, explicit way to move a service to new code. Resolves `--ref` to a concrete commit SHA against the service's `repo:` using `git ls-remote`, commits that SHA to the config repo, and triggers a reconcile. Defaults to the service's current `ref:`, else `HEAD`.

```bash
ownbasectl deploy mybase crm --ref v2.3.0
ownbasectl deploy mybase crm --ref main     # pins main's current tip SHA
ownbasectl deploy mybase crm --json         # {"status", "service", "ref"}
```

Because the committed ref is always a concrete SHA, a branch-named ref never silently redeploys when the branch moves.

### `service add|remove|update <name> <service> ...`

Structured, non-interactive edits to the `services:` map — a scriptable layer over the same client-side commit path. All three accept `--json`.

```bash
ownbasectl service add mybase crm --repo git@github.com:org/crm.git --port 3000 --domain crm.example.com
ownbasectl service update mybase crm --port 4000
ownbasectl service update mybase crm --domains crm.example.com,crm.example.org
ownbasectl service remove mybase crm
```

| Flag | Meaning |
|---|---|
| `--repo` | external git URL (required for `add`) |
| `--ref` | build ref; prefer `deploy` for version moves |
| `--dockerfile` / `--context` | Dockerfile path and build context subdirectory |
| `--port` | the port the container listens on |
| `--domain` / `--domains` | public hostname(s) |
| `--internal` | reachable only through `tunnel`, no Caddy route |
| `--data-path` | mount point for the service's data volume (default `/data`) |
| `--database` | `<provider-service>/<dbname>` to provision Postgres |
| `--requires` | capability dependencies |
| `--env` | `KEY=VALUE`, repeatable |
| `--add-capabilities` | Linux capabilities to restore |

`update` touches only the fields whose flags were passed. `--env` merges, with new values winning on a duplicate key; `--requires`, `--domains`, and `--add-capabilities` replace their lists entirely.

`--add-capabilities` restores capabilities after the compiler's default `DropCapability=ALL`. Only the minority of images that bind a port below 1024 need it — `NET_BIND_SERVICE` for something like `traefik/whoami` on port 80. Most images listen on 3000 or 8080 and never do.

`--database` asks the Base to create a Postgres database and hand the service its URL:

```bash
ownbasectl service add mybase api --repo git@github.com:org/api.git --port 8080 \
  --requires postgres --database postgres/revolve
```

The provider must also be in `--requires`. On the next reconcile the daemon creates the database if missing and writes `DATABASE_URL` into the service's secrets, so the credential is in neither the config repo nor the unit file. See [ownbase-yaml.md](ownbase-yaml.md#databases-database).

---

## `tunnel <name>`

Reach a Base's services at trusted local HTTPS URLs over SSH.

```bash
ownbasectl tunnel mybase
ownbasectl tunnel mybase --port 9443   # local bind port, default 8443
```

```
Tunneling:
  https://myapp.example.com.localhost:8443

No code-sync — push to your git host and deploy a ref to roll out changes.
Press Ctrl+C to stop.
```

It reads the Base's live `ownbase.yaml`, opens one SSH tunnel per service that has both a `port:` and a domain, and serves each at its real domain with `.localhost` appended. Per RFC 6761 any `.localhost` hostname resolves to loopback with no `/etc/hosts` entry and no DNS, so the URL survives IP changes and works offline. Traffic bypasses Caddy, so no port is opened on the Base.

Services marked `internal: true` are included even though they have no Caddy route — the tunnel is their only access path, which is the point. A service with no domain at all is never bridged.

This is the one command allowed to prompt: starting it is itself a human saying "I am sitting here", and the prompt is a one-time `mkcert -install` to trust a local certificate authority. The design rationale is in [decisions.md](decisions.md).

**There is no code-sync.** No bind mount, file watcher, or hot reload. Iterate the same way production does:

```bash
git push origin my-branch
ownbasectl deploy mybase <service> --ref my-branch
```

The tunnel picks up the new container transparently, since it tunnels to the service's port rather than to a container instance.

---

## Secrets

Per-service secrets, age-encrypted on the Base, injected as environment variables at container start.

```bash
ownbasectl secrets list mybase                  # services that have secrets
ownbasectl secrets list mybase myapp            # key names for one service
ownbasectl secrets get  mybase myapp DB_URL     # value; no trailing newline when piped
ownbasectl secrets set  mybase myapp DB_URL=postgres://... API_KEY=abc
ownbasectl secrets delete mybase myapp DB_URL
```

Plaintext travels only inside the SSH tunnel; the age private key never leaves the Base.

---

## Local commands

These operate on a checkout of a config repo and take no Base name. Mostly for development and previews.

| Command | Purpose | Flags |
|---|---|---|
| `compile` | Compile `ownbase.yaml` into `runtime/` (Quadlet units, Caddyfile, docker-compose.yml) | `--dir` (default `.`), `--out` (default `<dir>`) |
| `plan` | Show the diff between compiled desired state and what is running | `--dir`, `--fake-current` |
| `apply` | Apply the plan. A real apply needs Ubuntu + Podman | `--dir`, `--dry-run`, `--fake-current`, `--audit-log` |
| `version` | Print version, commit, and build date | — |

```
+ start  ownbase-auth
+ start  ownbase-crm
  skip   ownbase-postgres  (already running)
```

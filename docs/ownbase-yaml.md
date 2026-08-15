# `ownbase.yaml` reference

> The single declarative config file of a Base. It lives at the root of the Base's **external config repo** (e.g. a GitHub repo). Operators change the **tracked ref** client-side with `ownbasectl` (`config set` / `service add|update|remove` / `deploy`), which clones the config repo, edits `ownbase.yaml`, commits, and pushes with the operator's own git credentials, then asks the Base to pull and reconcile. Agents may push proposal branches via `POST /config` (`ownbase/agent/*` only); the Base applies only the tracked ref after merge.

## Full schema

```yaml
schema_version: v1 # required; only "v1" is understood

core:
  caddy:
    email: you@example.com # ACME contact email for automatic TLS
  backup:
    repo: s3:s3.amazonaws.com/my-bucket/ownbase # restic repository URL
    # interval: 1h          # optional, default 1h
    # verify_interval: 24h  # optional, default 24h
    # verify_postgres: true # optional, default true — recover a real Postgres in the drill

services:
  <name>:
    # Every service is built from an external git repo (repo:).
    repo: <external-git-url> # e.g. "git@github.com:org/app.git" or "https://github.com/docker-library/postgres"
    ref: <branch|tag|sha> # git ref to build from; set by `ownbasectl deploy`
    dockerfile: Dockerfile # optional; defaults to "Dockerfile"
    context: "" # optional build context subdirectory

    # Runtime
    port: <int> # container port; required for public domain
    domain: <hostname> # public domain → Caddy route (single-hostname form)
    domains: # OR: multiple public hostnames → one Caddy route each, same backend
      - <hostname>
      - <other-hostname>

    # Single-volume shorthand (backward compat)
    data_path: /data # mount path for the one named volume (default /data)

    # Multi-volume (use when a service needs separate volumes with different backup scopes)
    volumes:
      - name: config # Podman volume: ownbase-<service>-config
        mount: /config
        backup: ["."] # back up the entire volume
      - name: media
        mount: /media
        backup: ["./music"] # back up only selected subdirs
      - name: cache
        mount: /cache # omit backup: to exclude this volume entirely
      # When replicas: is set, volumes default to one copy per replica
      # (ownbase-<service>-<name>-0..N-1). Set per_replica: false for a
      # single volume mounted by every replica.
      # - name: workspaces
      #   mount: /workspaces
      #   per_replica: false
      #   backup: ["."]

    # Concurrency: run N indexed containers (ownbase-<name>-0..N-1).
    # Absent = single unindexed container (ownbase-<name>). When set, even
    # replicas: 1 is indexed. Not high availability — a Base is one machine.
    # replicas: 4

    env:
      - KEY=value # static environment variables

    # Dependencies
    requires:
      - <capability> # joins this service's capability network

    # A Postgres database the Base creates for this service, on the named
    # provider (which must also appear in requires:)
    database: postgres/<dbname>

    # Health check
    health_probe:
      http: /health # GET this path; 2xx = healthy

    # Container security
    user: "1000" # UID/username to run as; empty = image default
    add_capabilities: # caps to restore after DropCapability=ALL
      - NET_BIND_SERVICE # only set when the service genuinely needs them
    security_opt: # --security-opt flags; each entry widens the security boundary
      - apparmor=unconfined

    # Cgroup budget (Podman --memory / --cpus). Empty = unlimited.
    # Set on agent workers and anything that can runaway-allocate — a Base is
    # one machine and an uncapped pool can OOM the host.
    resources:
      memory: 4g
      cpus: "2"

    # Credentials the Base creates for you, on first reconcile, if missing
    generated_secrets:
      - type: password # a random value nobody needs to choose
        key: POSTGRES_PASSWORD
        # length: 32   # optional, default 32
      - type: ssh-ed25519 # a keypair, optionally split across two services
        public_key: CLIENT_PUBKEY
        private_key: other-service:SSH_KEY_B64
        private_encoding: base64 # or "raw" (default) for PEM as-is

    # Optional: daemon API access — see "Daemon API access" below.
    # ownbase_access:
    #   - status:read
    #   - config:write

jobs:
  <name>:
    service: <services-key> # required; reuses that service's image, networks, secrets
    command: ["python", "scripts/nightly_ingest.py"] # required; overrides the image's entrypoint/cmd
    schedule: "*-*-* 08:00:00 UTC" # required; systemd OnCalendar expression, e.g. "daily"
    env: # optional; appended after the referenced service's own env:
      - EXTRA_FLAG=1
    # persistent: true  # optional, default true — run once on boot if a scheduled run was missed
```

## The no-registry rule

`image:` is intentionally absent from user services. Every user service is **built locally on the Base** from a read-only clone of its `repo:` at the pinned `ref:` — no pre-built application images, ever. The core package (Caddy) is the only exception and is managed by `ownbasectl upgrade`, not by `ownbase.yaml`.

## Replicas: `replicas:`

When a service needs **concurrency** — several always-on workers with warm state, not high availability — set `replicas:`:

```yaml
services:
  opencode:
    repo: git@github.com:me/opencode-worker.git
    ref: abc123
    port: 4096
    internal: true
    domain: opencode.example.com   # for ownbasectl tunnel (replica 0 only)
    replicas: 4
    health_probe:
      http: /global/health
    volumes:
      - name: state
        mount: /home/opencode/.local/share/opencode
        backup: ["."]              # per-replica by default → four volumes backed up
      - name: workspaces
        mount: /workspaces
        per_replica: false         # one shared volume, all four mount it
        backup: ["."]
```

What OwnBase does:

| Piece | Behavior |
|---|---|
| Containers | `ownbase-opencode-0` … `ownbase-opencode-3`, each resolvable by Podman DNS |
| Image / secrets | One shared image and one secrets file (`opencode.yaml.age`) |
| Volumes | Per-replica by default (`ownbase-<svc>-<vol>-<i>`); `per_replica: false` for shared |
| Identity env | `OWNBASE_REPLICA_INDEX` and `OWNBASE_REPLICA_COUNT` injected into each replica |
| Health | Each replica has its own loopback publish; reconcile gates start on each probe |
| Caddy | Public replicated services get all replicas as `reverse_proxy` upstreams |
| Tunnel | `ownbasectl tunnel` bridges **replica 0 only** |
| Backup | Every per-replica volume with `backup:` is in the restic snapshot |
| Rolling replace | Sequential apply + health gate: replica *i* is healthy before *i+1* is touched |

What OwnBase does **not** do: session affinity, leasing, load-based placement, or autoscaling. Those belong in the application that talks to the workers (e.g. a harness with Postgres). See `docs/decisions.md`.

### Reaching replicas from another service

Podman DNS resolves **container names**, not service keys. A harness that `requires: [opencode]` joins the opencode capability network and should call:

```text
http://ownbase-opencode-0:4096
http://ownbase-opencode-1:4096
…
```

Discover *N* from config (`replicas:`) or from status (`replicas` / `running_replicas` on that service). Do not assume a single `ownbase-opencode` hostname when `replicas:` is set — that name only exists when the field is absent.

Each replica also receives:

| Env | Meaning |
|---|---|
| `OWNBASE_REPLICA_INDEX` | `0` … `N-1` for this container |
| `OWNBASE_REPLICA_COUNT` | `N` |

Use these in the worker image entrypoint when a URL must include the index (static `env:` cannot expand another variable):

```bash
export REDIS_URL="redis://ownbase-redis-${OWNBASE_REPLICA_INDEX}:6379"
exec opencode serve --hostname 0.0.0.0 --port 4096
```

### Companion services (cache, queue, “sidecar”)

There is no pod/sidecar type. A companion is another `services:` entry plus `requires:`.

**Shared (usual for Redis / cache / queue):** one companion, no `replicas:`; every worker uses the same hostname.

```yaml
services:
  redis:
    repo: git@github.com:me/redis.git
    ref: <sha>
    port: 6379
    volumes:
      - name: data
        mount: /data
        backup: ["."]
  opencode:
    replicas: 4
    requires: [redis]
    # workers use redis://ownbase-redis:6379
```

**Per-worker companion:** give the companion the **same** `replicas: N` and address by index in the worker entrypoint (`ownbase-redis-$OWNBASE_REPLICA_INDEX`). OwnBase does not enforce that pairing — keep *N* in sync yourself. Prefer shared unless you need isolation.

Cross-service volume mounts are still forbidden; companions talk over the network only.

### Rules of thumb

- **Absent `replicas:`** — single unindexed container `ownbase-<name>` (byte-identical to configs that predate the field).
- **`replicas: 1`** — still indexed (`ownbase-<name>-0`) so scaling 1→N never renames containers or orphans volumes.
- **Range** — 1..64 when set.
- **Name collision** — a service named `web-0` cannot coexist with `web` at `replicas: 2` (both would claim `ownbase-web-0`). Volume names are checked the same way: per-replica `state` index 0 collides with a shared volume named `state-0`.
- **Do not replicate database providers** in v1; `DATABASE_URL` and pgBackRest target the primary container only.
- **Jobs** are never multiplied by `replicas:`; they reuse the service image once.
- **Internal workers** — use `internal: true` plus a domain for tunnel inspection; put a public harness in front rather than exposing every replica on Caddy.

CLI: `ownbasectl service add … --replicas 4` and `ownbasectl service update … --replicas 4` (pass `--replicas 0` on update to clear the field).

## Daemon API access: `ownbase_access`

Opt a service into calling the daemon HTTP API over a private unix socket. Empty or absent = **no access** (default).

```yaml
services:
  harness:
    repo: git@github.com:org/harness.git
    port: 8080
    ownbase_access:
      - status:read
      - config:write
      - reconcile
```

When set, the daemon listens on `/run/ownbase/svc/<name>/api.sock` and the container bind-mounts that **directory** at `/run/ownbase/` (socket at `/run/ownbase/api.sock`). The socket path is the credential: whoever connects is that service principal. Scopes gate every route (**default-deny**). Host-mutating routes (`/config/source`, `/self-update`, `/token/reset`, …) are **owner-only even when `"*"` is granted**.

### Validation

- Non-empty strings; no duplicates
- Characters: `A–Z a–z 0–9 : _ - .`
- Allowed forms: exact scopes (`status:read`), trailing wildcards (`secrets:myapp:*`), or the literal `*`
- Bare `deploy` is accepted as a short form; other bare words without `:` are rejected

### Scopes that have HTTP routes today

| Scope | Effect |
|---|---|
| `status:read` | `GET /status`, `/version`, `/core/status`, `/db/status` |
| `config:read` | `GET /config` |
| `config:write` | `POST /config` → proposal branch `ownbase/agent/*` only |
| `reconcile` | `POST /reconcile` |
| `backup:run` / `backup:verify` | matching backup endpoints |
| `sshkey:read` | `GET /ssh-key` |
| `secrets:<svc>:read` / `secrets:<svc>:write` | per-service secrets routes |
| `*` | every grantable scope above — **includes every service's secrets** |

Some strings (`service:<name>:deploy`, `service:<name>:stop`) are accepted by schema validation for the taxonomy checkpoint but have **no HTTP route** yet — declaring them does nothing on the socket until a route is added.

Normative route table, owner-only list, and curl examples: [api.md — Service access over unix sockets](api.md#service-access-over-unix-sockets-ownbase_access).

### Security notes

- Prefer the minimum scopes. A service that needs only health visibility should get `status:read`, not `*`.
- `*` is total for grantable routes: it can read and write every service's secrets via the API.
- A service that **requires** the daemon API to function is less portable off a Base (see the [Service Constitution](foundation/service-constitution.md)); design so the service degrades without the socket when possible.

## Public domains: `domain:` and `domains:`

A service becomes publicly reachable once it has **both** a `port:` and at least one domain — the compiler emits one Caddy route per domain, all pointing at the same container:port:

```yaml
services:
  app:
    repo: git@github.com:org/app.git
    port: 3000
    domains: # serve the same service under two hostnames
      - app.example.com
      - app.example.org
```

`domain:` (singular) still works exactly as before — it is simply folded into the same effective domain list (`EffectiveDomains()`), so existing configs need no migration. Use `domains:` when a service needs more than one public hostname; there is no need to switch existing single-domain services to `domains:`.

A service with **no** domain configured (`domain:` and `domains:` both empty — the default for a newly added service) is internal-only: Caddy has no route for it, and — since a Base with no domain'd service anywhere exposes only SSH externally (see `docs/decisions.md`, "SSH tunnel bridge") — it is not reachable from outside the Base at all. Reach it locally with `ownbasectl tunnel` instead (below).

To define a service that has a domain for tunnel routing but is **intentionally never internet-facing**, set `internal: true`:

```yaml
services:
  admin:
    repo: git@github.com:org/admin.git
    port: 3000
    domain: admin.example.com
    internal: true   # tunnel-only — no Caddy route, never reachable from the internet
```

An `internal: true` service is reachable via `ownbasectl tunnel` at `https://admin.example.com.localhost:8443`, but the compiler emits no Caddy route for it, so it is never accessible from the internet even if DNS points at the Base.

## Local HTTPS via tunnel (`ownbasectl tunnel`)

A fresh Base has no domain configured anywhere, so it never opens 80/443 and Caddy never gets a real Let's Encrypt certificate — there's no way to see it over trusted HTTPS the way a real deployed Base would be seen. `ownbasectl tunnel <name>` solves this without touching `create`, which must stay perfectly agent-safe: zero prompts, ever.

```bash
ownbasectl tunnel mybase
```

This is the one command in `ownbasectl` allowed to prompt interactively (a one-time `sudo mkcert -install`, ever, on this machine). It reads the Base's live `ownbase.yaml` over SSH, opens one SSH tunnel per service that has both a `port:` and a domain configured — a service with no domain is never bridged — and serves each at its real domain with `.localhost` appended, e.g. `domain: myapp.example.com` → `https://myapp.example.com.localhost:8443`, a locally-trusted HTTPS URL that works fully offline and never changes across a VM restart. Services marked `internal: true` are included. See `docs/cli.md` for the full command reference and `docs/decisions.md` for the design rationale.

**There is no code-sync mechanism** — `ownbasectl tunnel` only tunnels and proxies traffic to whatever is currently deployed. To iterate on a service's code, push to the service's `repo:` on your git host and run `ownbasectl deploy <base> <name> --ref <branch>` (see "Updates: the `ref:` model" below); the tunnel, if still running, picks up the new container transparently.

## `repo:` — how services are sourced

`repo:` is always an **external git URL** — the daemon keeps a read-only `git clone --bare --mirror` of it locally under `/opt/ownbase/repos/<service-name>` (keyed by the service name, so two services can safely point at the same upstream):

```yaml
repo: git@github.com:org/auth.git                     # SSH (private repos)
repo: https://github.com/docker-library/postgres      # anonymous HTTPS
```

Private repos are cloned using the Base's managed SSH identity (see [cli.md](cli.md), `ssh-key`): run `ownbasectl ssh-key <base> add --host github.com`, then register the printed public key as a **read-only deploy key** on each **service** repo. For the **config** repo, use write only if agents should call `POST /config`, and keep branch protection on the tracked ref. There is no push-to-Base *service* source path — the Base never hosts service code, it only clones from your git host.

## Updates: the `ref:` model

A service moves to new code only when the operator runs `ownbasectl deploy`:

```bash
ownbasectl deploy mybase auth --ref v1.1.0   # tag, branch, or commit
```

`deploy` resolves the requested ref to a concrete commit SHA against the service's `repo:` (client-side, via `git ls-remote`), writes that SHA into `ownbase.yaml`, commits + pushes it to the config repo, and triggers a reconcile. Because the committed `ref:` is always a concrete SHA, deploys are deterministic and reproducible — there is no server-side branch-tip pinning and no automatic blank-ref resolution.

- **Branch-named refs never auto-redeploy.** `deploy` is the sole path to move a service; a service pinned to a branch does not follow that branch's tip until you deploy again. This is intentional ("explicit only").
- **Drift visibility.** `ownbasectl updates` shows commits-behind and the newest semver tag for every service (see [cli.md](cli.md)).
- **Deprecated: `mode:`.** The field is still parsed (so old configs don't break) but has no effect; a warning is emitted when present. Remove it.

## What the daemon does on every reconcile

1. Fetches the external config repo into the checkout at `/opt/ownbase/checkout` (`internal/configsource`) — hard-reset to the tracked ref
2. Reads `ownbase.yaml` and compiles the desired state (Quadlet units, Caddyfile)
3. Opens/syncs per-service API socket directories for every `ownbase_access` grant **before** containers apply (`prepareAccess`)
4. Ensures a local bare clone exists for every service, cloning each `repo:` on first sight. Full commit SHAs skip fetch when already local; **branch and tag refs are always refetched** (`internal/repos`)
5. Generates any missing `generated_secrets:` (`internal/gensecrets`) and creates any declared `database:`, writing its `DATABASE_URL` (`internal/gendb`) — both before the compile, so a new value reaches the container in this cycle rather than the next
6. Checks for drift (compiler output vs. `runtime/` on disk)
7. Queries what Podman/systemd is actually running
8. Diffs desired vs. actual → produces a `PlannedAction` list
9. For each service: checks out its local bare clone at `ref:` and runs `podman build`
10. Applies the plan — each action is checkpoint-authorized and audit-logged
11. Re-syncs access sockets/grants to match live config; updates the `/status` API

Reconciles are triggered explicitly by `ownbasectl` (`deploy`, `config set`, `service *`, `config setup`) via `POST /reconcile`, or after a proposal branch is merged; a periodic timer backstop also runs as a safety net.

## Backups: `core.backup:`

| Key | Default | Meaning |
|---|---|---|
| `repo` | *(unset — backups disabled)* | restic repository URL (`s3:`, `b2:`, `sftp:`, or a local path for dev) |
| `interval` | `1h` | how often a snapshot is taken |
| `verify_interval` | `24h` | how often the verified-restore drill runs |
| `verify_postgres` | `true` | whether the drill recovers a real Postgres from the backed-up pgBackRest repository |
| `append_only` | `false` | when true, scheduled snapshots do **not** run `restic forget --prune`; apply retention with `ownbasectl backup prune` using delete-capable cloud keys that never live on the Base |

Credentials do not go here — they live in `/opt/ownbase/secrets/backup.yaml.age`, set by `ownbasectl backup setup` (or `secrets set <base> backup AWS_…=…` for cloud keys only; `RESTIC_PASSWORD` must go through `backup setup` / `backup rekey`). Which volumes reach the repository is decided per service by `volumes[].backup:`.

**Append-only mode.** A compromised Base that holds delete-capable cloud keys can wipe the restic repository. With `append_only: true` the keys on the Base should be non-deleting. Retention is applied by the owner via `ownbasectl backup prune`, which sends delete-capable credentials through the SSH tunnel for one invocation and never stores them on the Base. Optional vault escrow: `--admin-aws-…` / `--admin-b2-…` on setup or `backup prune --escrow`.

#### Non-deleting S3 key (Base / snapshot path)

Mint two IAM users (or one user with two access keys): one for the Base, one for prune. Attach the Base key a policy like this (replace bucket and prefix):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ResticReadWriteNoDelete",
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject",
        "s3:ListBucket",
        "s3:GetBucketLocation",
        "s3:ListBucketMultipartUploads",
        "s3:AbortMultipartUpload",
        "s3:ListMultipartUploadParts"
      ],
      "Resource": [
        "arn:aws:s3:::my-bucket",
        "arn:aws:s3:::my-bucket/ownbase/*"
      ]
    },
    {
      "Sid": "ResticClearOwnLocks",
      "Effect": "Allow",
      "Action": "s3:DeleteObject",
      "Resource": "arn:aws:s3:::my-bucket/ownbase/locks/*"
    }
  ]
}
```

Restic must delete its own lock files; without the second statement snapshots hang on stale locks. The prune/admin key keeps unrestricted `s3:DeleteObject` (and the same Get/Put/List set) on the prefix so `backup prune` can run `forget --prune`. Prefer a dedicated backup bucket the Base never holds delete rights on.

**B2 / other S3-compatible APIs.** B2 application keys cannot express a path-scoped delete exception as cleanly as IAM. Practical options: (1) use a full-capability key only in the vault admin escrow and keep the Base on a read/write key if the provider allows it, or (2) accept that B2 append-only is weaker than S3 IAM and rely on bucket versioning. Check your provider's key model before treating append-only as ransomware-proof.

**Belt and braces.** Enable bucket versioning (and, where available, object-lock / compliance retention) on the backup bucket so even a delete-capable key cannot silently erase history.

### What the verified-restore drill proves

`Restorable` is not set because backups ran; it is set because a restore worked. On its `verify_interval` (or on demand via `ownbasectl checkup <base> --verify`) the daemon restores the newest snapshot into a throwaway directory, checks it, and tears it down:

1. **`restic check --read-data-subset=5%`** — the repository is internally consistent and its pack data actually reads back.
2. **File presence** — every path that was backed up came back. A path that does not exist on this Base passes vacuously, since restic skips a nonexistent source rather than failing the snapshot.
3. **Postgres recovery** — when the restore contains a pgBackRest repository, the drill restores that stanza into a throwaway Postgres built from the same image production runs, waits for WAL replay to finish, and asks the recovered database for its catalog.

The third check is the one that makes the claim meaningful. The first two prove the files came back, which is a much weaker statement than the database came back: a pgBackRest repository can restore cleanly and still fail to recover — a gap in the WAL archive, a full backup that aged out from under its incrementals, a server version that cannot read what the client wrote — and none of that is visible to a file-level check.

It cannot touch production. The repository it recovers is the restic-restored copy in a temporary directory, the container has no network, Postgres listens on nothing but a Unix socket inside it, and `archive_mode` is forced off so the promoted throwaway cluster cannot push WAL into a backup repository. Set `verify_postgres: false` to skip it when the CPU and minutes are genuinely a problem — at the cost of leaving the recovery path untested until the day it is needed.

## Secrets

Per-service secrets never live in `ownbase.yaml` or the config repo. Each service's secrets are stored on the Base as a single [age](https://github.com/FiloSottile/age)-encrypted file at `/opt/ownbase/secrets/<service>.yaml.age`, decrypted only in memory by the daemon and injected into the service's container as environment variables at start.

```bash
ownbasectl secrets set mybase myapp DB_URL=postgres://... API_KEY=abc
ownbasectl secrets get mybase myapp DB_URL
```

The age private key (`/opt/ownbase/age/key.age`) never leaves the Base; plaintext values travel only inside the SSH tunnel between `ownbasectl` and the daemon. There is one age recipient per Base — no multi-key sharing, no external KMS. This is a deliberate simplicity choice over formats like `sops`: the file is opaque as a whole (no per-field structure to inspect), which is sufficient because the daemon is the only consumer and rotation just re-encrypts the (small) file.

### Generated secrets: `generated_secrets:`

Some credentials have no business being authored by a human. An SSH keypair cannot be written in YAML at all, and a database password an operator invents is a password they are tempted to reuse. So `ownbase.yaml` names the keys and leaves the values to the Base:

```yaml
services:
  pgbackrest:
    repo: https://github.com/ownbase-ai/pgbackrest
    generated_secrets:
      - type: ssh-ed25519
        public_key: PGBACKREST_CLIENT_PUBKEY # stored on this service (the end that accepts)
        private_key: postgres:PGBACKREST_SSH_KEY_B64 # stored on postgres (the end that dials)
        private_encoding: base64

  postgres:
    repo: https://github.com/ownbase-ai/pgbackrest
    generated_secrets:
      - type: password
        key: POSTGRES_PASSWORD
```

On each reconcile the daemon generates whatever is missing and stores it in the same age-encrypted per-service files as `ownbasectl secrets set`, from which it is injected as a container environment variable. Two properties make this safe to run on every tick:

- **It only ever fills gaps.** A key that already has a value is left alone, so restarts never rotate a credential and you can always override a generated value by setting it by hand. A keypair is all-or-nothing: if one half is already present, neither half is regenerated, since a mismatched pair would authenticate against nothing.
- **Generation happens on the Base.** A private key never crosses the network nor touches your disk, and a rebuilt Base regenerates what it needs without anyone having to remember what was there before.

Destinations are written as `KEY` (this service) or `service:KEY` (another service, which must exist), so the two halves of a keypair land on the two ends of the connection that uses them. `private_encoding: base64` exists because a PEM private key does not survive a trip through an environment variable intact, and most images that read a key from the environment expect the single-line form.

## Databases: `database:`

A service that needs its own Postgres database says so, naming the service that provides it:

```yaml
services:
  postgres:
    repo: https://github.com/ownbase-ai/pgbackrest
    context: postgres
    port: 5432
    env:
      - POSTGRES_USER=ownbase
      - POSTGRES_DB=ownbase
    generated_secrets:
      - type: password
        key: POSTGRES_PASSWORD

  api:
    repo: git@github.com:org/api.git
    port: 8080
    requires: [postgres] # required: this is what joins the two containers
    database: postgres/revolve
```

On each reconcile the daemon creates `revolve` if it does not exist, and writes a `DATABASE_URL` into `api`'s secrets file — composed from the provider's `POSTGRES_USER`, its generated `POSTGRES_PASSWORD`, and the provider's container name and port:

```
postgresql://ownbase:<password>@ownbase-postgres:5432/revolve
```

From there it reaches the container as an environment variable through the normal secrets path, so the credential is in neither the config repo nor the unit file. Nothing is written to `ownbase.yaml`.

The provider is named rather than inferred, and must also appear in `requires:`. Two separate things are being said: `requires:` joins the containers to a network and orders their startup, `database:` is what gets created. A Base can run two Postgres services, and nothing here depends on one of them happening to be called `postgres`.

Like generated secrets, this converges rather than acts: a database that exists is left alone, and a URL that already matches is not rewritten, so a Base that has been up for a month does no work here and its containers are not restarted. Two consequences worth knowing:

- **A rotated provider password rewrites the URL.** Declaring `database:` says OwnBase owns `DATABASE_URL` for that service; a service whose URL should be managed by hand simply does not declare it.
- **The first reconcile of a fresh Base is too early.** Creating a database needs the provider's Postgres to be running, which it is not until later in the same cycle. The daemon logs what it skipped and finishes the job on the next tick, about a minute later.

## Scheduled jobs: `jobs:`

A job runs a command on a recurring schedule — a nightly feed import, a periodic cleanup script — by reusing an existing service's already-built image rather than declaring (and building) anything of its own:

```yaml
services:
  api:
    repo: git@github.com:org/api.git
    port: 8000
    domain: api.example.com

jobs:
  nightly-ingest:
    service: api # must match a services: key
    command: ["python", "scripts/nightly_ingest.py", "--region", "mx"]
    schedule: "*-*-* 08:00:00 UTC" # systemd OnCalendar — see systemd.time(7)
    env:
      - REVOLVE_FEED_URL_MX=https://example.com/feed.csv
```

- **Image, networks, and hardening come from `service:`.** The job runs `localhost/ownbase-<service>:local` — the exact image the service itself runs — on the same capability networks, so it can reach the service's own dependencies (e.g. Postgres) by hostname. It never triggers a build of its own.
- **`command:` replaces the image's entrypoint/cmd** for this run only; the service container itself is unaffected.
- **`env:` is appended after the service's own `env:`,** and a job's own secrets (`ownbasectl secrets set <base> <job-name> ...`) are merged on top of the referenced service's secrets, so a job automatically inherits the service's DB/API credentials and can override any individual one without redeclaring the rest.
- **`schedule:` is a systemd `OnCalendar` expression** (`daily`, `"*-*-* 08:00:00 UTC"`, `"Mon..Fri *-*-* 09:00:00"`, ...). The compiler renders it into a native systemd `.timer` unit — installed to the host's systemd unit directory, not the Quadlet directory, since a timer isn't a Quadlet type — that activates the job's oneshot container on that schedule.
- **Jobs are never started by reconcile itself.** A job container compiles with `Type=oneshot`, `Restart=no`, and no `[Install]` section, and reconcile only ever (re)installs the unit file — the timer (or a manual `systemctl start ownbase-job-<name>.service` on the Base) is the only thing that actually runs it. This is deliberate: unlike a long-running service, "not currently running" is a job's normal resting state between activations, not a failure to correct.
- **v1 jobs get no volume mounts.** A job is expected to be a stateless batch/script; if it needs durable storage, mount it on the referenced service instead.
- **Status:** `ownbasectl status <base>` shows each job's schedule, whether its timer is enabled, and the last run's outcome (`ownbase_status.jobs[]` in the JSON API).

## Integrating a new service (the black-box contract)

Any service can be integrated by following [integration-contract.md](integration-contract.md). The short version, done non-interactively with `ownbasectl`:

```bash
ownbasectl service add mybase auth --repo git@github.com:org/auth.git --port 8080 --domain auth.example.com
ownbasectl deploy mybase auth --ref main
```

Or the same steps by hand:

1. **Add the repo** — set `repo:` to the external git URL. For a private repo, register the Base's deploy key first (`ownbasectl ssh-key <base> add --host github.com`).
2. **Declare it** — `port:`, `data_path:` (or `volumes:`), `requires:`
3. **Ensure a Dockerfile** in the repo root (or set `dockerfile:`/`context:` for non-standard layouts)
4. **Run the Service Constitution audit** — [foundation/service-constitution.md](foundation/service-constitution.md)
5. **Deploy** — `ownbasectl deploy <base> <name> --ref <ref>`; the daemon builds it locally and brings it up health-gated

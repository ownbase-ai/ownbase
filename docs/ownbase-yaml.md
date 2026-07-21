# `ownbase.yaml` reference

> The single declarative config file of a Base. It lives at the root of the Base's **external config repo** (e.g. a GitHub repo). Operators change it client-side with `ownbasectl` (`config set` / `service add|update|remove` / `deploy`), which clones the config repo, edits `ownbase.yaml`, commits, and pushes with the operator's own git credentials, then asks the Base to pull and reconcile. The daemon has **read-only** access to the config repo.

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

    env:
      - KEY=value # static environment variables

    # Dependencies
    requires:
      - <capability> # joins this service's capability network

    # Health check
    health_probe:
      http: /health # GET this path; 2xx = healthy

    # Container security
    user: "1000" # UID/username to run as; empty = image default
    add_capabilities: # caps to restore after DropCapability=ALL
      - NET_BIND_SERVICE # only set when the service genuinely needs them

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

## Local HTTPS via tunnel (`ownbasectl tunnel`) {#local-https-during-development-ownbasectl-tunnel}

A fresh Base has no domain configured anywhere, so it never opens 80/443 and Caddy never gets a real Let's Encrypt certificate — there's no way to see it over trusted HTTPS the way a real deployed Base would be seen. `ownbasectl tunnel <name>` solves this without touching `create`/`vm` (which must stay perfectly agent-safe: zero prompts, ever):

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

Private repos are cloned using the Base's managed SSH identity (see [cli.md](cli.md), `ssh-key`): run `ownbasectl ssh-key <base> add --host github.com`, then register the printed public key as a **read-only deploy key** on the repo. There is no push-to-Base source path — the Base never hosts service code, it only clones from your git host.

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

1. Fetches the external config repo into the read-only checkout at `/opt/ownbase/checkout` (`internal/configsource`)
2. Reads `ownbase.yaml` and compiles the desired state (Quadlet units, Caddyfile)
3. Ensures a local bare clone exists for every service, cloning each `repo:` on first sight and fetching any pinned `ref:` not yet present locally (`internal/repos`)
4. Checks for drift (compiler output vs. `runtime/` on disk)
5. Queries what Podman/systemd is actually running
6. Diffs desired vs. actual → produces a `PlannedAction` list
7. For each service: checks out its local bare clone at `ref:` and runs `podman build`
8. Applies the plan — each action is checkpoint-authorized and audit-logged
9. Updates the `/status` API with the new state

Reconciles are triggered explicitly by `ownbasectl` (`deploy`, `config set`, `service *`, `config setup`) via `POST /reconcile`; a periodic timer backstop also runs as a safety net.

## Secrets

Per-service secrets never live in `ownbase.yaml` or the config repo. Each service's secrets are stored on the Base as a single [age](https://github.com/FiloSottile/age)-encrypted file at `/opt/ownbase/secrets/<service>.yaml.age`, decrypted only in memory by the daemon and injected into the service's container as environment variables at start.

```bash
ownbasectl secrets set mybase myapp DB_URL=postgres://... API_KEY=abc
ownbasectl secrets get mybase myapp DB_URL
```

The age private key (`/opt/ownbase/age/key.age`) never leaves the Base; plaintext values travel only inside the SSH tunnel between `ownbasectl` and the daemon. There is one age recipient per Base — no multi-key sharing, no external KMS. This is a deliberate simplicity choice over formats like `sops`: the file is opaque as a whole (no per-field structure to inspect), which is sufficient because the daemon is the only consumer and rotation just re-encrypts the (small) file.

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

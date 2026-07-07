# `ownbase.yaml` reference

> The single declarative config file of a Base. It lives at the root of the Base's config repo — a local, remote-less bare git repo at `/opt/ownbase/repo`. Pushing a change (via `ownbasectl config set`/`service add|update|remove`, or a direct `git push` over SSH) triggers a `post-receive` hook → reconcile.

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
    # Source-built service (built from a local bare repo the user pushes into)
    source: <local-repo-path> # e.g. "services/auth" or "myorg/crm"
    ref: <branch|tag|sha> # git ref to build from; blank = auto-pin to latest commit
    dockerfile: Dockerfile # optional; defaults to "Dockerfile"
    context: "" # optional build context subdirectory

    # OR: mirror-built (daemon clones + maintains a local bare mirror)
    mirror: <external-git-url> # e.g. "https://github.com/docker-library/postgres"

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
```

## The no-registry rule

`image:` is intentionally absent from user services. Every user service is **built locally on the Base** from a local bare repo at the pinned `ref:` — no pre-built application images, ever. The core package (Caddy) is the only exception and is managed by `ownbasectl upgrade`, not by `ownbase.yaml`.

## Public domains: `domain:` and `domains:`

A service becomes publicly reachable once it has **both** a `port:` and at least one domain — the compiler emits one Caddy route per domain, all pointing at the same container:port:

```yaml
services:
  app:
    source: apps/app
    port: 3000
    domains: # serve the same service under two hostnames
      - app.example.com
      - app.example.org
```

`domain:` (singular) still works exactly as before — it is simply folded into the same effective domain list (`EffectiveDomains()`), so existing configs need no migration. Use `domains:` when a service needs more than one public hostname; there is no need to switch existing single-domain services to `domains:`.

A service with **no** domain configured (`domain:` and `domains:` both empty — the default for a newly added service) is internal-only: Caddy has no route for it, and — since a Base with no domain'd service anywhere exposes only SSH externally (see `docs/decisions.md`, "Local development") — it is not reachable from outside the Base at all. Reach it locally with `ownbasectl dev` instead (below).

## Local HTTPS during development (`ownbasectl dev`)

A fresh Base has no domain configured anywhere, so it never opens 80/443 and Caddy never gets a real Let's Encrypt certificate — there's no way to see it over trusted HTTPS the way a real deployed Base would be seen. `ownbasectl dev <name>` solves this without touching `create`/`vm` (which must stay perfectly agent-safe: zero prompts, ever):

```bash
ownbasectl dev mybase
```

This is the one command in `ownbasectl` allowed to prompt interactively (a one-time `sudo mkcert -install`, ever, on this machine). It reads the Base's live `ownbase.yaml` over SSH, opens one SSH tunnel per service that has both a `port:` and a domain configured — a service with no domain is never bridged — and serves each at its real domain with `.localhost` appended, e.g. `domain: myapp.example.com` → `https://myapp.example.com.localhost:8443`, a locally-trusted HTTPS URL that works fully offline and never changes across a VM restart. See `docs/cli.md` for the full command reference and `docs/decisions.md` for the design rationale.

**There is no code-sync mechanism** — `ownbasectl dev` only tunnels and proxies traffic to whatever is currently deployed. To iterate on a service's code, use the same git-push-to-deploy flow as production: push a branch to the service's bare repo and run `ownbasectl service update <base> <name> --ref <branch>` (see "Updates: the `ref:` model" below); the dev bridge, if still running, picks up the new container transparently.

## `source:` paths — how they work

`source:` is always a **local bare repo path** under `/opt/ownbase/repos/`, never a URL:

```yaml
source: services/auth    # → /opt/ownbase/repos/services/auth
source: myorg/crm        # → /opt/ownbase/repos/myorg/crm
```

There is no reserved "apps" vs. "services" split, and no notion of an org on the Base itself — the path is just a directory nesting under `/opt/ownbase/repos/`. Every entry under `services:` in `ownbase.yaml` is declared and built the same way regardless of the path.

The daemon (`internal/repos`) creates an empty bare repo at that path the first time the service is declared; the user (or an agent) then pushes real content into it directly over SSH, exactly like the config repo — `git push ssh://<user>@<base>/opt/ownbase/repos/services/auth <branch>`. To track a GitHub repo instead, declare it with `mirror:` — the daemon clones it into a local bare mirror and builds from there. Never put a GitHub URL directly in `source:`.

## Updates: the `ref:` model

Updates are user-driven — edit `ref:` and commit (by hand, or with `ownbasectl service update <base> <name> --ref <new-ref>`):

```yaml
services:
  auth:
    ref: v1.0.0 # edit this to v1.1.0 and commit to update
```

Committing the change triggers the normal reconcile: if the new `ref:` isn't already present in the service's local bare repo, the daemon fetches it from the external URL (`mirror:` services only — `source:` services are pushed into directly, so the ref is already there), then rebuilds and restarts the service health-gated. No silent mutations, no daemon-opened PRs.

- **Blank `ref:` auto-pin.** A service with no `ref:` gets the default-branch HEAD commit SHA resolved and committed back to `ownbase.yaml` automatically — a concrete, reproducible pin without looking up the SHA by hand. Deleting `ref:` is therefore "give me the latest, then pin it".
- **Drift visibility.** `ownbasectl updates` shows commits-behind and the newest semver tag for every service (see [cli.md](cli.md)).
- **Deprecated: `mode:`.** The field is still parsed (so old configs don't break) but has no effect; a warning is emitted when present. Remove it.

## What the daemon does on every push

1. Pulls the latest commit from the config bare repo into the checkout
2. Reads `ownbase.yaml` and compiles the desired state (Quadlet units, Caddyfile)
3. Ensures a local bare repo exists for every service, cloning `mirror:` services on first sight and fetching any pinned `ref:` not yet present locally (`internal/repos`)
4. Checks for drift (compiler output vs. `runtime/` on disk)
5. Queries what Podman/systemd is actually running
6. Diffs desired vs. actual → produces a `PlannedAction` list
7. For each service: clones its local bare repo at `ref:` and runs `podman build`
8. Applies the plan — each action is checkpoint-authorized and audit-logged
9. Updates the `/status` API with the new state

## Secrets

Per-service secrets never live in `ownbase.yaml` or the config repo. Each service's secrets are stored on the Base as a single [age](https://github.com/FiloSottile/age)-encrypted file at `/opt/ownbase/secrets/<service>.yaml.age`, decrypted only in memory by the daemon and injected into the service's container as environment variables at start.

```bash
ownbasectl secrets set mybase myapp DB_URL=postgres://... API_KEY=abc
ownbasectl secrets get mybase myapp DB_URL
```

The age private key (`/opt/ownbase/age/key.age`) never leaves the Base; plaintext values travel only inside the SSH tunnel between `ownbasectl` and the daemon. There is one age recipient per Base — no multi-key sharing, no external KMS. This is a deliberate simplicity choice over formats like `sops`: the file is opaque as a whole (no per-field structure to inspect), which is sufficient because the daemon is the only consumer and rotation just re-encrypts the (small) file.

## Integrating a new service (the black-box contract)

Any service can be integrated by following [integration-contract.md](integration-contract.md). The short version, done non-interactively with `ownbasectl`:

```bash
ownbasectl service add mybase auth --mirror https://github.com/org/auth --ref main --port 8080 --domain auth.example.com
```

Or the same steps by hand:

1. **Add the repo** — add a `mirror:` entry (the daemon clones a local bare mirror automatically), or use `source:` and push real content into the empty bare repo the daemon creates for you
2. **Declare it** — `ref:` (or omit to auto-pin), `port:`, `data_path:` (or `volumes:`), `requires:`
3. **Ensure a Dockerfile** in the repo root (or set `dockerfile:`/`context:` for non-standard layouts)
4. **Run the Service Constitution audit** — [foundation/service-constitution.md](foundation/service-constitution.md)
5. **Push** — the daemon builds it locally and brings it up health-gated

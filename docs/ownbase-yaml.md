# `ownbase.yaml` reference

> The single declarative config file of a Base. It lives at the root of the Base's config repo (on the Base's own Forgejo); committing a change triggers `git push` → `post-receive` hook → reconcile.

## Full schema

```yaml
schema_version: v1 # required; only "v1" is understood

core:
  forgejo:
    domain: git.yourdomain.com # public domain for the Forgejo UI (optional)
  caddy:
    email: you@example.com # ACME contact email for automatic TLS
  backup:
    repo: s3:s3.amazonaws.com/my-bucket/ownbase # restic repository URL
    # interval: 1h          # optional, default 1h
    # verify_interval: 24h  # optional, default 24h

services:
  <name>:
    # Source-built service (built from a local Forgejo repo)
    source: <forgejo-repo-path> # e.g. "services/auth" or "myorg/crm"
    ref: <branch|tag|sha> # git ref to build from; blank = auto-pin to latest commit
    dockerfile: Dockerfile # optional; defaults to "Dockerfile"
    context: "" # optional build context subdirectory

    # OR: mirror-built (daemon creates + maintains the Forgejo pull-mirror)
    mirror: <external-git-url> # e.g. "https://github.com/docker-library/postgres"

    # Runtime
    port: <int> # container port; required for public domain
    domain: <hostname> # public domain → Caddy route

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

`image:` is intentionally absent from user services. Every user service is **built locally on the Base** from a Forgejo repo at the pinned `ref:` — no pre-built application images, ever. Core packages (Forgejo, Caddy) are the only exception and are managed by `ownbasectl upgrade`, not by `ownbase.yaml`.

## `source:` paths — how they work

`source:` is always a **Forgejo repo path**, never a URL:

```yaml
source: services/auth    # → Forgejo repo owned by the "services" org
source: myorg/crm        # → Forgejo repo owned by the "myorg" org
```

The org name is an arbitrary Forgejo org — there is no reserved "apps" vs. "services" split; every entry under `services:` in `ownbase.yaml` is declared and built the same way regardless of which org its repo lives in.

The daemon calls `<forgejo-url>/api/v1/repos/<org>/<repo>` to clone the repo at the pinned `ref:`. The org is the path's first component. To track a GitHub repo, declare it with `mirror:` — the daemon mirrors it into Forgejo and builds from there. Never put a GitHub URL directly in `ownbase.yaml`.

## Updates: the `ref:` model

Updates are user-driven — edit `ref:` and commit:

```yaml
services:
  auth:
    ref: v1.0.0 # edit this to v1.1.0 and commit to update
```

Committing the change triggers the normal reconcile: the service is rebuilt from the repo at the new `ref:` and restarted health-gated. No silent mutations, no daemon-opened PRs.

- **Blank `ref:` auto-pin.** A service with no `ref:` gets the default-branch HEAD commit SHA resolved and committed back to `ownbase.yaml` automatically — a concrete, reproducible pin without looking up the SHA by hand. Deleting `ref:` is therefore "give me the latest, then pin it".
- **Drift visibility.** `ownbasectl updates` shows commits-behind and the newest semver tag for every service (see [cli.md](cli.md)).
- **Deprecated: `mode:`.** The field is still parsed (so old configs don't break) but has no effect; a warning is emitted when present. Remove it.

## What the daemon does on every push

1. Pulls the latest commit from the bare repo into the checkout
2. Reads `ownbase.yaml` and compiles the desired state (Quadlet units, Caddyfile)
3. Ensures Forgejo pull-mirrors exist for all `mirror:` services
4. Checks for drift (compiler output vs. `runtime/` on disk)
5. Queries what Podman/systemd is actually running
6. Diffs desired vs. actual → produces a `PlannedAction` list
7. For source-built services: clones the Forgejo repo at `ref:` and runs `podman build`
8. Applies the plan — each action is checkpoint-authorized and audit-logged
9. Updates `OWNBASE.md` and the `/status` API with the new state

## Secrets

Per-service secrets never live in `ownbase.yaml` or the config repo. Each service's secrets are stored on the Base as a single [age](https://github.com/FiloSottile/age)-encrypted file at `/opt/ownbase/secrets/<service>.yaml.age`, decrypted only in memory by the daemon and injected into the service's container as environment variables at start.

```bash
ownbasectl secrets set mybase myapp DB_URL=postgres://... API_KEY=abc
ownbasectl secrets get mybase myapp DB_URL
```

The age private key (`/opt/ownbase/age/key.age`) never leaves the Base; plaintext values travel only inside the SSH tunnel between `ownbasectl` and the daemon. There is one age recipient per Base — no multi-key sharing, no external KMS. This is a deliberate simplicity choice over formats like `sops`: the file is opaque as a whole (no per-field structure to inspect), which is sufficient because the daemon is the only consumer and rotation just re-encrypts the (small) file.

## Integrating a new service (the black-box contract)

Any service can be integrated by following [integration-contract.md](integration-contract.md). The short version:

1. **Mirror the repo** — add a `mirror:` entry (the daemon creates the Forgejo pull-mirror automatically), or use `source:` for a repo that already lives on the Base's Forgejo
2. **Declare it** — `ref:` (or omit to auto-pin), `port:`, `data_path:` (or `volumes:`), `requires:`
3. **Ensure a Dockerfile** in the repo root (or set `dockerfile:`/`context:` for non-standard layouts)
4. **Run the Service Constitution audit** — [foundation/service-constitution.md](foundation/service-constitution.md)
5. **Push** — the daemon builds it locally and brings it up health-gated

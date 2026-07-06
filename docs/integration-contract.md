# Black-box Service Integration Contract

> **What this doc decides:** the exact, reusable specification for how a service is added to a Base. Once this contract is clear, any service — default or alternative — can be integrated the same way, without special-casing in the OwnBase spine.

---

## The unit of integration: a local repo, built locally

A service is added as a **local bare git repo built locally to a `localhost/ownbase-<name>` image**. Nothing is pulled from a registry to deploy an application service.

There are two ways to declare a service in `ownbase.yaml`:

1. **`source:`** — an empty bare repo under `/opt/ownbase/repos/` that the user (or an agent, via `ownbasectl`) pushes into directly over SSH — exactly like the config repo.
2. **`mirror:`** — an external git URL (GitHub, any public host); OwnBase clones it into a local bare mirror named `mirrors-<basename>` and builds from it. Declarative: the operator only specifies the URL; the daemon manages the mirror, fetching new refs on demand.

**The no-registry rule:** `image:` and `digest:` are not valid user service fields. The core package (Caddy) is the only bootstrap exception and is managed by the installer, not by `ownbase.yaml`.

```text
upstream repo (GitHub, any git host) — mirror: declaration in ownbase.yaml
        │  OwnBase clones a local bare mirror (mirrors-<basename>)
        ▼
local bare repo (/opt/ownbase/repos/<name>)  @  pinned ref:
        │  daemon clones + builds at ref:
        ▼
localhost/ownbase-<name>:local   (on-Base image cache only)
        │  Quadlet unit starts the container
        ▼
running service
```

The Base reasons about the service through its **repo + Dockerfile**, never its internals. It does not need to know what language, framework, or runtime the service uses. The Dockerfile is the only build interface.

---

## The integration surface: `ownbase.yaml` entry

Added to the Base's `ownbase.yaml` by the operator:

```yaml
services:
  auth:                              # service instance name
    source: services/auth            # local bare repo path (or use mirror: for external repos)
    ref: v1.0.0                      # pinned branch, tag, or commit SHA (omit to auto-pin to latest)
    port: 8080                       # container port; all public traffic routes here
    domain: auth.example.com         # optional: public hostname (Caddy provisions TLS)
    data_path: /data                 # mount path for the persistent data volume
    requires:
      - postgres                     # joins the postgres capability network
    health_probe:
      http: /health                  # optional: GET path; 2xx = healthy
```

`source:` is **always a local bare repo path** — never a URL. To track an external repo, add a `mirror:` entry and let the daemon create the local mirror automatically. Either way, the entry above can be added, changed, or removed non-interactively with `ownbasectl service add/update/remove` — see [cli.md](cli.md).

`ref:` is the single pinning mechanism: `repo @ ref:` → same Dockerfile → same build → same image.

All external traffic routes to the declared `port:` via Caddy. No routing configuration is needed — the service just needs to listen on its port.

Persistent data is stored in a Podman named volume `ownbase-<name>-data`, mounted at `data_path:` (defaults to `/data`). Declare `data_path:` when the service writes data elsewhere.

---

## The Dockerfile is the build interface

The daemon clones the repo at `ref:` and runs `podman build` from the Dockerfile. No other contract is required:

- No separate manifest file
- No registry push
- No build server

For monorepos or versioned layouts, use `context:` to point at a subdirectory (e.g. `context: "17/alpine"` for docker-library/postgres).

---

## Isolation guarantees ([architecture-principles.md](foundation/architecture-principles.md), principle 13)

OwnBase enforces these unconditionally for every service:

| Property | Mechanism |
|---|---|
| Rootless container | Podman rootless; no root process |
| Per-service user namespace | Podman user namespace isolation |
| Per-capability network | Service joins only the networks of its declared `requires:` |
| Scoped secrets | Service receives only the secrets in `/opt/ownbase/secrets/<name>.yaml.age`; scoping is structural, not policy-based |
| No shared runtime socket | No Docker/Podman socket passed into containers |
| Own data volume | `ownbase-<name>-data` is isolated; not shared with other services |

---

## Lifecycle

The standard OwnBase lifecycle applies to every service:

```text
Build          daemon clones the repo at ref:; runs podman build -t localhost/ownbase-<name>:local
Start          systemctl start ownbase-<name>.service (Quadlet unit)
Health-gate    daemon probes health_probe.http until 2xx
Reconcile      on every git push or timer tick
Update         user edits ref: in ownbase.yaml and commits; ownbasectl updates shows drift
Backup         data volume included in the restic snapshot on every backup interval
Restore        verified restore drill confirms data is recoverable
Explain        service appears in the status API (ownbasectl status)
```

---

## Service Constitution compliance

Every service must satisfy all five rules of the [Service Constitution](foundation/service-constitution.md):

1. **Removable** — removing from `ownbase.yaml` stops and tears down the service
2. **Forkable** — source is in a local bare repo the user owns and can modify (push directly, or fork the `mirror:` URL and repoint it)
3. **Replaceable** — services depend on the capability name (`requires:` key), not the specific provider
4. **Data accessible** — data is in a standard Podman volume the user can access
5. **Works standalone** — image is built locally; nothing to reach outside the Base at runtime

---

## Core infrastructure exception

Caddy (the reverse proxy) cannot be built from a local repo at bootstrap time — the Base needs it to exist before it can route to anything. It is the single narrow exception to the no-registry rule: it is installed from a digest-pinned public image embedded in the OwnBase binary (`internal/core`), never declared in `ownbase.yaml`, and updated only via `ownbasectl upgrade`.

This exception does not apply to any other service. Everything declared under `services:` is a local bare repo built locally.

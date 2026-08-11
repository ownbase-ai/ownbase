# Identity and authority

> Who may act on a Base, how they prove it, and what they are allowed to do. Canonical home for **principal**, **scope**, **checkpoint**, **owner-only route**, **tracked ref**, and **proposal branch**. Operational tables live in [api.md](../api.md); declaration syntax in [ownbase-yaml.md](../ownbase-yaml.md#daemon-api-access-ownbase_access).

## Two principals

Every daemon action and every audit record carries a **principal** — the subject of the authorization decision.

| Principal | How it authenticates | Default power |
|---|---|---|
| **Owner** | Bearer token on the TCP loopback API (`/opt/ownbase/api-token`), used by `ownbasectl` and the desktop app over an SSH tunnel | Full taxonomy under current policy (no external approval device yet) |
| **Service** | Unix socket path for that service (`/run/ownbase/svc/<name>/api.sock`). The socket *is* the credential — no Bearer | Only the scopes listed in that service's `ownbase_access:`; default-deny |

There is no anonymous principal on management routes. `GET /health` is the only unauthenticated endpoint (on both TCP and socket).

Code: `internal/schema.Principal`, `Owner()`, `Service(name)`.

## Scopes and grants

A **scope** is a closed string that names a grantable capability (e.g. `status:read`, `config:write`, `secrets:myapp:read`). A service declares scopes under `ownbase_access:`; that list is its **grant**. Matching rules:

- Exact equality
- Trailing wildcard: `secrets:myapp:*` matches `secrets:myapp:read` and `secrets:myapp:write`
- Literal `*` matches every grantable scope — **still not owner-only routes**

Scopes gate HTTP routes on the socket path (normative table: [api.md](../api.md#service-access-over-unix-sockets-ownbase_access)). Some scope strings exist for the taxonomy checkpoint (`service:<name>:deploy`) but have no HTTP route yet.

## Checkpoints

Every taxonomy action passes through a **checkpoint** before it runs (`internal/authz`):

| Checkpoint | Who | Rule |
|---|---|---|
| `OwnerCheckpoint` | Owner | Every taxonomied action is authorized |
| `GrantCheckpoint` | Service | Must have a grant entry; `TierApprove` always refused; action must map to a granted scope |
| `CompositeCheckpoint` | Both | Dispatches by principal kind |

An action type not in the taxonomy cannot execute. An action without a scope mapping is refused for services.

## Owner-only routes

Some HTTP routes never become grantable, even with `*`:

- `POST /config/source` — repoint the Base at a different config repo
- `POST /token/reset`, `POST /self-update`, `POST /upgrade`
- `POST /security/*`, `POST /db/restore`
- `POST /ssh-key`
- `GET /secrets/` (list all services)

These stay owner-only because their blast radius is the whole machine or the operator's git history witness, not a single service. Adding a new host-mutating route without mapping it in `authz.RouteAccess` leaves it owner-only by default — that is intentional default-deny.

## Tracked ref vs proposal branch

| Term | Meaning |
|---|---|
| **Tracked ref** | The branch (or tag/SHA) the Base reconciles from — usually `main`, recorded in `/opt/ownbase/config-source.yaml` and the vault profile. Operators push here with their own git credentials via `ownbasectl`. |
| **Proposal branch** | A branch under `ownbase/agent/*` pushed by the daemon via `POST /config`. Never applied until merged on the forge into the tracked ref. |

The Base **never** pushes the tracked ref. Branch protection on the forge is the merge gate. After merge, `POST /reconcile` (or the next timer) applies it.

Two mutation paths, one source of truth:

1. **Operator** — `ownbasectl` clones the config repo client-side, edits, commits to the tracked ref, `POST /reconcile`
2. **Agent** — service with `config:write` (or owner Bearer) calls `POST /config` → proposal branch → human/auto-merge on forge → reconcile

## Vault pin

The vault profile's `ConfigRepoURL` / `ConfigRef` is the operator's **pin** of which config repo this Base should track — a trust anchor, not a cache. `status` / `checkup` may backfill an *empty* pin from the Base, but never overwrite a non-empty pin when the Base reports something different. A mismatch is a security signal (a rooted Base can rewrite `config-source.yaml`). Intentional repoint: `ownbasectl config setup`. Rebuild: `restore` re-asserts the vault pin onto the Base before reporting success.

## Related

- Isolation defaults: [architecture-principles.md](architecture-principles.md) §13
- Taxonomy and tiers: [architecture-principles.md](architecture-principles.md) §14
- Locked choices: [decisions.md](../decisions.md) (Config authority, Status API)
- Agent workflow: [operating.md](../operating.md)

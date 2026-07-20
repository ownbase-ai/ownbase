# Architecture Principles

> How the system is shaped at a technical level. Durable constraints, not an implementation spec — dated specifics (which container runtime, which Git host) live in code and [docs/decisions.md](../decisions.md). When an engineering decision is in question, it must satisfy every principle here.

The mental model for the whole system:

> A **managed GitOps installation**, not a black-box platform.
> The user's server is the runtime.
> The user's Git repo is the source of truth.
> OwnBase is the operator that keeps it secure, updated, backed up, and understandable.

## The principles

### 1. Git is the source of truth

The desired state of a Base lives in a Git repository the user owns — an external repo on their own git host, which the operator commits to client-side and the Base clones read-only. The server reconciles itself toward that repo. Nothing important exists only in a vendor database. If OwnBase disappeared, the repo plus the machine is a complete, working system.

More precisely: the **ownership invariant** is `reconstructable = (repo, secrets, backups)` — own these three and you can reconstruct anything. The **operational model** is `reconcile(compile(repo, secrets), current)`, where for a rebuild `current` begins as `restore(backups)` rather than the running state. This is what makes install, update, recover, and rebuild the same `reconcile` call from different starting conditions. See [reconstruction-model.md](reconstruction-model.md).

### 2. The repo beats a database

Where state could live in a vendor database or in the user's repo, it lives in the repo. Caches and indexes are allowed but must be *derivable* and non-authoritative. The authoritative copy of config and service code is always the user's.

### 3. Plain files over proprietary formats

Configuration and state are human-readable files (YAML, plain text, standard formats) — never opaque blobs only our tools can open. A user (or their AI) should be able to open any file and understand what it does. See [service-constitution.md](service-constitution.md), rule 4.

### 4. Reversible over irreversible

Prefer decisions a user can undo. The user controls updates by editing `ref:` and committing — no service changes without a commit they authored. Risky actions snapshot first. Anything destructive is gated, logged, and recoverable.

### 5. Legible layout over magical locations

The system lives where you would look for it, organized so a human or AI can understand it at a glance. Representative on-machine layout:

```text
/opt/ownbase/
  repo/        # bare git remote — the irreducible source of truth
  checkout/    # working checkout the daemon reconciles from
  runtime/     # generated runtime files — never hand-edited
  data/        # persistent application and service data
  backups/     # local backup staging
  logs/        # service and daemon logs, structured health
  bin/         # the OwnBase daemon (supervisor)
  age/         # the secret decryption key
```

And a representative user-owned repo:

```text
ownbase/
  system/      # hardening, firewall, monitoring, backups, caddy, container runtime
  services/    # every service — default capability providers (auth, jobs, the on-Base git host, ...)
               # and the user's own software — declared and built the same way
  secrets/     # never committed in plaintext — see ownbase-yaml.md, "Secrets"
  ownbase.yaml # the control file
  README.md    # the operating guide, seeded at install — how to work on this Base safely
```

### 6. One control file is the contract

A single file, `ownbase.yaml`, is the contract between the CLI, the daemon, and any AI. The user (or their AI) edits intent at a high level; the daemon compiles the boring details.

Everything on a Base is a **repo**: a Postgres instance, an auth service, and a custom CRM each declare an external `repo:` git URL, which the daemon keeps a read-only clone of under `/opt/ownbase/repos/` on the Base. There is no catalog of pre-built images — everything is built locally from source. See [ownbase-yaml.md](../ownbase-yaml.md) for the full schema.

Generated artifacts (the `runtime/` units, the generated Caddyfile) are derived from `ownbase.yaml` by a deterministic compiler and must never be the place a human edits intent.

### 7. Capabilities, not implementations

Services depend on capabilities (`auth`, `jobs`, `storage`) rather than on specific products. A capability is satisfied by a provider, and providers are swappable. This is what makes services genuinely replaceable and is enforced by [service-constitution.md](service-constitution.md).

### 8. Compose-shaped, systemd-owned; never Kubernetes

A single-machine container primitive is the right one for this user: simple, inspectable, AI-editable, widely understood, easy to recover. The runtime is **Compose-shaped, systemd-owned via Podman Quadlet** — rootless, daemonless containers whose lifecycle (restart, dependency ordering, health) systemd owns.

The human or AI never edits the generated units — they edit `ownbase.yaml` (principle 6). Kubernetes and general-purpose orchestrators are explicitly out of scope; this prohibition is absolute.

### 9. The user drives updates; the daemon reports drift

The user (or their AI) updates a service by editing `ref:` in `ownbase.yaml` and committing. No service changes without a commit they authored. The daemon never opens unsolicited update PRs.

The daemon does two things to support this:

1. **Resolve blank refs.** When a service has no `ref:`, the daemon commits the default-branch HEAD SHA back to `ownbase.yaml` — a concrete, reproducible pin. After that, the ref is frozen until the user changes it.
2. **Report drift.** On its update interval, the daemon computes how far behind each service's pinned ref is from its source and surfaces this in `ownbasectl updates`. The daemon does not act on this information — it only informs.

```text
user edits ref: -> commits -> hook -> reconcile -> BUILD from repo@ref -> deploy
                                                  daemon: resolve blank -> commit -> hook -> reconcile -> deploy
daemon (periodic):  compute drift -> report via ownbasectl updates
```

### 10. The daemon has four jobs

The OwnBase daemon is the operator on the machine. It does exactly four things:

1. **Reconcile** — make the machine match the repo; apply approved changes.
2. **Watch** — security, uptime, disk, certs, exposed ports, backups, logs.
3. **Explain** — produce status that is readable by both humans and AI (the `/status` API behind `ownbasectl status`/`checkup`).
4. **Recover** — roll back, restore, restart, rotate secrets, close unsafe ports.

If a proposed capability does not fit one of these four jobs, question whether the daemon should do it at all.

Reconcile is **event-driven**: a commit to the source of truth is the trigger, and a periodic timer is only a drift backstop.

### 11. The AI interface is a first-class artifact

Any AI must be able to operate a Base without guessing, through two surfaces that are part of the product and maintained with the same care as any user-facing one:

- **The config repo is self-describing.** Its seeded README carries the operating contract (how to make a change, what never to touch) and `ownbase.yaml` declares every service, what it requires, and how it is reached. Intent lives in git.
- **Live state is served, not committed.** What is actually running, healthy, and secure comes from the `/status` API (`ownbasectl status`/`checkup`) — always current, never a stale snapshot in the repo. Observed state does not belong in the source of truth (principle 1: the repo declares intent).

### 12. Durable by design, not highly available

A Base is one machine, and one machine is a single point of failure: a disk, a provider outage, or a bad kernel can take it offline. We are honest about this rather than implying uptime we do not provide. The commitment is **durability, not availability**:

- **Data is never lost.** Backups are continuous and off-machine, verified by actually restoring them, not just confirming they ran.
- **Recovery is fast and rehearsed.** Restores are drilled so that when a machine dies, a new one comes up from the repo plus the latest verified backup quickly, with no data loss.
- **Uptime is best-effort for a single Base.** We do not promise multi-nines on one machine.

"We will never lose your data" is a promise backed by a real, testable mechanism (`ownbasectl backup`, `ownbasectl restore`). "You will never be down" is a promise a single machine cannot make, and we do not make it.

### 13. Isolation limits blast radius

Every service the user deploys — especially AI-generated code — runs with the minimum surface area necessary. Colocation on one machine is a performance and cost advantage, but it also means a compromised or misbehaving service could reach the database, the secrets vault, or other services if left unchecked. We manage that risk structurally, not by trusting the code:

- **Rootless, per-service containers, least privilege.** Each service runs in its own rootless container (no privileged daemon, no shared runtime socket) with no more Linux capabilities than it needs. No service mounts volumes from another service or from system directories.
- **Scoped secrets.** Each service receives only the secrets it is declared to need. No service can enumerate or read another service's secrets. Secrets are age-encrypted and injected at start — see [ownbase-yaml.md](../ownbase-yaml.md), "Secrets".
- **Internal network segmentation.** Services reach declared capabilities (auth, jobs, database) over per-capability networks only; they cannot reach the host runtime, the daemon, or other services' private ports by default.

If any principle here would be violated by a proposed change, redesign the change before shipping.

### 14. Every daemon action is taxonomy-checked and audited

Every action the daemon takes is drawn from a closed taxonomy (`internal/schema/taxonomy.go`) and pre-classified into a risk tier — **autonomous** (act immediately), **notify** (act and tell the owner), or **approve** (suspend until approved). Today every tier resolves to autonomous in practice — there is no external approval device yet — but the taxonomy and the checkpoint the daemon passes every action through already exist, so tightening the policy later is additive, not a rewrite. Every action is recorded in an audit log the user owns and can export or delete at any time. An action type that isn't in the taxonomy cannot be executed.

## The standard against which to judge any design

> If OwnBase disappeared tomorrow, the user would still have a working, understandable Ubuntu machine with all their code, data, and services intact — and could keep running everything without us.

Any architecture that cannot honestly make that claim is wrong, no matter how convenient it is.

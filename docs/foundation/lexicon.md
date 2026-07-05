# Lexicon

> Shared meaning of OwnBase terms, so humans and AI agents use words the same way. When a word elsewhere is ambiguous, this is the definition.

## Core nouns

**OwnBase** : The system that turns a machine the user controls into a secure, self-maintaining home for the software they own.

**a Base** : One user's installation of OwnBase — a single owned machine, everything on it, and its config repo. "Create a Base," "the Base is healthy." Plural: Bases.

**the daemon** (`ownbased`) : The OwnBase process that runs on a Base and operates it. It does exactly four things: reconcile, watch, explain, recover. See [architecture-principles.md](architecture-principles.md), principle 10.

**`ownbasectl`** : The command-line tool that creates, connects to, and operates a Base from your machine — over an SSH tunnel to the daemon's API. See [cli.md](../cli.md).

**Service** : A piece of software declared under `services:` in `ownbase.yaml` and run on a Base — whether it's a default OwnBase capability provider (auth, jobs, a database) or software the user built themselves. There is no separate "app" concept: everything running on a Base is a service, declared and built the same way, and every service obeys [service-constitution.md](service-constitution.md). A service can depend on other services' capabilities via `requires:`.

**Capability** : An abstract function a service can depend on — `auth`, `jobs`, `storage`. A capability is satisfied by a *provider*, and providers are swappable. Services reference the capability, never the implementation.

**Provider** : A specific implementation that satisfies a capability (e.g. `ownbase-auth` satisfies `auth`; `authentik` could too). Swapping providers must not require rewriting the services that depend on it.

## Operating concepts

**Reconcile** : The daemon making the machine match the desired state in the config repo. Event-driven: a commit is the trigger, a periodic timer is only a drift backstop. Install, update, recover, and rebuild are all `reconcile` from different starting conditions. See [reconstruction-model.md](reconstruction-model.md).

**The compiler / runtime artifacts** : The deterministic step that turns `ownbase.yaml` into the generated `runtime/` artifacts the daemon applies. Pure, byte-reproducible, single-writer. The `runtime/` files are never hand-edited.

**Drift** : A difference between what `ownbase.yaml` declares and what is actually running — reported by `ownbasectl updates`/`checkup`, never silently auto-corrected against the user's `ref:` intent. Also used for a change in generated `runtime/` files that the compiler did not make (a tamper signal), since the compiler is the only thing that may write `runtime/`.

**Verified recovery (verified restore)** : Restoring the latest backup into an ephemeral, isolated environment, running integrity checks, and tearing it down — so "restorable" is a measured property (`ownbasectl backup status`), not a claim.

**Genesis record** : The signed record the first reconcile writes into the repo — machine fingerprint, daemon version, pinned image digests — a reproducible description of what was installed that recovery diffs against.

**Watch** : The daemon's continuous monitoring of security, uptime, disk, certs, exposed ports, backups, and logs.

**Explain** : The daemon producing status readable by both humans and AI (the `/status` API, rendered by `ownbasectl status`/`checkup`).

**Recover** : The daemon restoring health — rollback, restore, restart, secret rotation, closing unsafe ports.

**`ownbase.yaml`** : The single control file in the user's repo that defines core settings and services. The contract between `ownbasectl`, the daemon, and any AI. See [ownbase-yaml.md](../ownbase-yaml.md).

**The config repo README** : The operating guide seeded into each Base's config repo at install — the first thing a human or AI sees when they open the repo, telling them exactly how to work on the Base safely. Static by design: live state comes from the `/status` API, not from git.

**Durability (vs. availability)** : The reliability commitment for a single Base is durability — data is never lost and is restorable — not high availability. See [architecture-principles.md](architecture-principles.md), principle 12.

**Risk tier (autonomous / notify / approve)** : The classification of every daemon action by reversibility and blast radius, defined in the action taxonomy (`internal/schema/taxonomy.go`). Every action executed today resolves to autonomous in effect — there is no external approval step yet — but every action already carries a tier and is audit-logged. See [architecture-principles.md](architecture-principles.md), principle 14.

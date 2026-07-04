# Foundation

The durable rules of how a Base works — not strategy, not roadmap. Written for humans and for the LLM agents that operate or modify OwnBase. When two documents seem to conflict, [architecture-principles.md](architecture-principles.md) wins.

- [lexicon.md](lexicon.md) — canonical definitions (Base, Agent, capability, reconcile, risk tiers). Read this first if a term is unclear anywhere else.
- [architecture-principles.md](architecture-principles.md) — the technical constraints that don't change: Git as source of truth, plain files, no Kubernetes, the reconcile model, on-machine layout.
- [service-constitution.md](service-constitution.md) — the five rules every service must satisfy (removable, forkable, replaceable, data accessible, works standalone).
- [reconstruction-model.md](reconstruction-model.md) — the core invariant: a Base is fully described by `(repo, secrets, backups)`. Install, update, recover, and rebuild are all the same reconcile operation.
- [base-lifecycle.md](base-lifecycle.md) — the ten lifecycle stages (create → operate → update → back up → recover → retire) mapped to the CLI commands that perform each one.

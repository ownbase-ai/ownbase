# Foundation

The durable rules of how a Base works — not strategy, not roadmap. Written for humans and for the LLM agents that operate or modify OwnBase. The *why* behind all of it lives in [MISSION.md](../../MISSION.md), which wins any conflict; among the documents here, [architecture-principles.md](architecture-principles.md) wins.

- [lexicon.md](lexicon.md) — canonical definitions (Base, Agent, capability, reconcile, risk tiers). Read this first if a term is unclear anywhere else.
- [architecture-principles.md](architecture-principles.md) — the technical constraints that don't change: Git as source of truth, plain files, no Kubernetes, the reconcile model, on-machine layout.
- [service-constitution.md](service-constitution.md) — the five rules every service must satisfy (removable, forkable, replaceable, data accessible, works standalone).
- [reconstruction-model.md](reconstruction-model.md) — the core invariant: a Base is fully described by `(repo, secrets, backups)`. Install, update, recover, and rebuild are all the same reconcile operation.
- [base-lifecycle.md](base-lifecycle.md) — the nine lifecycle stages (create → secure → build → deploy → observe → update → recover → export → retire) mapped to the CLI commands that perform each one.

## Canonical source per claim

Key claims are deliberately repeated across documents so each is self-contained for AI readers. The cost is drift: when a claim changes, every restatement must change with it. This table tracks where to edit *first* and which other docs to audit afterward.

| Claim | Canonical source | Also restated in |
|---|---|---|
| The hard constraints (six) | [MISSION.md](../../MISSION.md) | README, AGENTS.md |
| `reconstructable = (repo, secrets, backups)`; four ops, one reconcile | [reconstruction-model.md](reconstruction-model.md) | README, architecture-principles §1, this file |
| Secrets model (age-encrypted, key never leaves, injected at start) | [ownbase-yaml.md](../ownbase-yaml.md) "Secrets" | decisions, api, cli, reconstruction-model, architecture-principles §13 |
| `ref:` update model + explicit `ownbasectl deploy` | [ownbase-yaml.md](../ownbase-yaml.md) "Updates" | architecture-principles §9, decisions, cli, operating |
| Verified restore ("restorable" is measured, not claimed) | [reconstruction-model.md](reconstruction-model.md) | README, architecture-principles §12, decisions, cli, lexicon |
| The five service rules (removable, forkable, replaceable, data accessible, standalone) | [service-constitution.md](service-constitution.md) | integration-contract, lexicon, MISSION.md |
| No-registry rule + core-package exception | [ownbase-yaml.md](../ownbase-yaml.md) "The no-registry rule" | integration-contract, decisions, architecture-principles §6 |
| Isolation / blast-radius model | [architecture-principles.md](architecture-principles.md) §13 | integration-contract |
| Action taxonomy + risk tiers, all autonomous today | [architecture-principles.md](architecture-principles.md) §14 | decisions, lexicon |
| Operating rules (read the config repo first; mutate only via `ownbase.yaml` + commit) | [operating.md](../operating.md) | AGENTS.md, README "How a Base works", the seeded config-repo README |
| Tier-1 / Tier-2 test workflow | [development.md](../development.md) | README "Testing" |

When editing a canonical source, check the "Also restated in" docs for the same claim and update them to stay consistent — or add a forward reference and trim the restatement if it is now redundant.

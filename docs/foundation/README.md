# Foundation

The durable rules of how a Base works — not strategy, not roadmap. Written for humans and for the LLM agents that operate or modify OwnBase. The *why* behind all of it lives in [MISSION.md](../../MISSION.md), which wins any conflict; among the documents here, [architecture-principles.md](architecture-principles.md) wins.

- [lexicon.md](lexicon.md) — canonical definitions (Base, Agent, capability, reconcile, risk tiers, principal). Read this first if a term is unclear anywhere else.
- [architecture-principles.md](architecture-principles.md) — the technical constraints that don't change: Git as source of truth, plain files, no Kubernetes, the reconcile model, on-machine layout.
- [identity-and-authority.md](identity-and-authority.md) — who may act (owner vs service), scopes, checkpoints, tracked ref vs proposal branch, vault pin.
- [service-constitution.md](service-constitution.md) — the five rules every service must satisfy (removable, forkable, replaceable, data accessible, works standalone).
- [reconstruction-model.md](reconstruction-model.md) — the core invariant: a Base is fully described by `(repo, secrets, backups)`. Install, update, recover, and rebuild are all the same reconcile operation.
- [base-lifecycle.md](base-lifecycle.md) — the nine lifecycle stages (create → secure → build → deploy → observe → update → recover → export → retire) mapped to the CLI commands that perform each one.

## Canonical source per claim

Key claims are deliberately repeated across documents so each is self-contained for AI readers. The cost is drift: when a claim changes, every restatement must change with it. This table tracks where to edit *first* and which other docs to audit afterward.

| Claim | Canonical source | Also restated in |
|---|---|---|
| Positioning: the tagline, the thesis that AI builds faster when it owns the whole machine, and ownership as the mechanism rather than a second feature | [MISSION.md](../../MISSION.md) | README front matter, AGENTS.md "One sentence" |
| What "own everything" includes — down to OwnBase itself, MIT-licensed and forkable | [MISSION.md](../../MISSION.md) | README "Why build here", AGENTS.md, service-constitution "The ownership boundary", lexicon |
| The hard constraints (six) | [MISSION.md](../../MISSION.md) | README "Why build here", AGENTS.md |
| `reconstructable = (repo, secrets, backups)`; four ops, one reconcile | [reconstruction-model.md](reconstruction-model.md) | architecture-principles §1, this file |
| Secrets model (age-encrypted, key never leaves, injected at start) | [ownbase-yaml.md](../ownbase-yaml.md) "Secrets" | decisions, api, cli, reconstruction-model, architecture-principles §13 |
| `ref:` update model + explicit `ownbasectl deploy` | [ownbase-yaml.md](../ownbase-yaml.md) "Updates" | architecture-principles §9, decisions, cli, operating |
| Verified restore ("restorable" is measured, not claimed) | [reconstruction-model.md](reconstruction-model.md) | architecture-principles §12, decisions, cli, lexicon |
| The five service rules (removable, forkable, replaceable, data accessible, standalone) | [service-constitution.md](service-constitution.md) | integration-contract, lexicon, MISSION.md |
| No-registry rule + core-package exception | [ownbase-yaml.md](../ownbase-yaml.md) "The no-registry rule" | integration-contract, decisions, architecture-principles §6 |
| Isolation / blast-radius model | [architecture-principles.md](architecture-principles.md) §13 | integration-contract, ownbase-yaml `ownbase_access`, api.md sockets |
| Action taxonomy + risk tiers (service principals constrained; owner approve device still post-V1) | [architecture-principles.md](architecture-principles.md) §14 | decisions, lexicon, identity-and-authority |
| Principals, scopes, checkpoints, owner-only routes | [identity-and-authority.md](identity-and-authority.md) | api.md, ownbase-yaml, lexicon, decisions |
| Config authority: operator tracked-ref path vs agent proposal branches | [decisions.md](../decisions.md) "Config authority" | api.md POST /config, operating, identity-and-authority, reconstruction-model, README, INSTALL, cli, AGENTS |
| Deploy-key permissions (read on service repos; write on config repo only if agents propose; branch protection) | [INSTALL.md](../../INSTALL.md#two-different-ssh-keys) | README setup, cli `ssh-key`, api `/ssh-key`, decisions SSH identity, AGENTS |
| Vault config-source pin (trust anchor; mismatch is a signal; restore re-asserts) | [vault.md](../vault.md) | identity-and-authority, cli restore/status, troubleshooting, reconstruction-model |
| Operating rules (read the config repo first; mutate only via `ownbase.yaml` + commit or POST /config proposals) | [operating.md](../operating.md) | AGENTS.md, README "Operating a Base", the seeded config-repo README |
| Tier-1 / Tier-2 test workflow | [development.md](../development.md) | INSTALL.md "Contributors" |
| Setup flow (keygen → user creates server → create --remote) | [README.md](../../README.md#setting-up-a-base) | INSTALL.md, AGENTS.md job 1, base-lifecycle §1 |
| Provisioning design (key resolution, preflight, `--wait`, exit codes) | [decisions.md](../decisions.md) "Provisioning a Base" | INSTALL.md, cli |
| Tunnel design (`.localhost` scheme, port allocation, no code-sync) | [decisions.md](../decisions.md) "SSH tunnel bridge" | cli |
| Postgres PITR behaviour (recovery window, archiver health, what to do) | [troubleshooting.md](../troubleshooting.md) "Postgres point-in-time recovery" | cli, api, decisions "Point-in-time recovery" |
| Machine sizing and measured capacity | [README.md](../../README.md#how-big-a-machine) | INSTALL.md, troubleshooting |

When editing a canonical source, check the "Also restated in" docs for the same claim and update them to stay consistent — or add a forward reference and trim the restatement if it is now redundant.

The split between the last four rows is worth stating: **decisions.md owns *why* a mechanism is shaped as it is, cli.md owns *what the flags do*, and troubleshooting.md owns *what to do when it misbehaves*.** A page that finds itself explaining a mechanism's rationale is usually restating one of the other two.

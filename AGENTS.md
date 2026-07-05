# AGENTS.md

> Dispatch for AI agents working with OwnBase. Two jobs come through here: **operating** a user's Base, and **modifying** the OwnBase code itself (the daemon and `ownbasectl`). Find your job below and follow the doc that owns it.

## One sentence

> AI makes it easy to build software. OwnBase makes it easy to own it.

OwnBase turns a user-controlled Ubuntu machine (their **Base**) into a secure, self-maintaining home for AI-built services. The full why — and the hard constraints every change must respect (user owns everything, nothing is mysterious, operations disappear, every service is ownable, boring technology, no pre-built images) — lives in [MISSION.md](MISSION.md). Do not violate a hard constraint without the user's explicit direction.

When in doubt: does the change make the user **more of an owner** and **less of a sysadmin**? If not, it is probably the wrong change.

## Job 1: Operating a Base

You have SSH/CLI access to a running Base and are asked to change what's deployed, diagnose a problem, or check health.

**Start with [docs/operating.md](docs/operating.md)** — the order of operations (read the config repo first; the only mutation path is `ownbase.yaml` + a commit to the Base's own Forgejo; never hand-edit `runtime/`). Then:

| Need | Doc |
|---|---|
| What's deployed on *this* Base | `ownbase.yaml` + README in the Base's config repo; `ownbasectl status` for live state |
| `ownbase.yaml` schema, `ref:` updates, secrets | [docs/ownbase-yaml.md](docs/ownbase-yaml.md) |
| CLI command reference | [docs/cli.md](docs/cli.md) |
| Daemon HTTP API | [docs/api.md](docs/api.md) |
| Adding any service (the black-box contract) | [docs/integration-contract.md](docs/integration-contract.md) |
| Something failed | [docs/troubleshooting.md](docs/troubleshooting.md) |
| Removing OwnBase / exporting everything | [docs/uninstall.md](docs/uninstall.md) |

## Job 2: Modifying the OwnBase code itself

You are changing `cmd/ownbased`, `cmd/ownbasectl`, or `internal/` — the Go source that becomes the daemon and the CLI.

**Start with [docs/development.md](docs/development.md)** — build/test workflow (Tier-1 anywhere, Tier-2 on the Ubuntu VM), the invariants to preserve (idempotency, deterministic compiler, single writer to `runtime/`, taxonomy-audited actions, no plaintext secrets on disk, honest dry-runs), and the merge gate. Then:

| Need | Doc |
|---|---|
| Why the code is the way it is (locked choices) | [docs/decisions.md](docs/decisions.md) — check before "fixing" anything that looks wrong |
| The durable rules of how a Base works | [docs/foundation/](docs/foundation/) — read once, in order |
| Canonical term definitions | [docs/foundation/lexicon.md](docs/foundation/lexicon.md) |
| Install / fresh-install verification | [INSTALL.md](INSTALL.md) |

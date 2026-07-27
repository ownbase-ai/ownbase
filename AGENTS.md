# AGENTS.md

> Dispatch for AI agents working with OwnBase. Three jobs come through here: **setting up** a Base, **operating** one, and **modifying** the OwnBase code itself. Find your job below and follow the doc that owns it.

## One sentence

> Build faster with AI. Own everything.

AI builds faster when it owns the whole machine. A **Base** is one machine the user controls, running OwnBase, where an agent has root and can therefore read logs, inspect processes, and verify its own work — instead of calling whatever endpoints a vendor chose to expose. The user owns all of it: the server, the code, the data, the config, the secrets, the backups, the domains, and OwnBase itself, which is MIT-licensed and forkable.

The full why — and the hard constraints every change must respect (user owns everything, nothing is mysterious, operations disappear, every service is ownable, boring technology, no pre-built images) — lives in [MISSION.md](MISSION.md). Do not violate a hard constraint without the user's explicit direction.

When in doubt: does this make the user **more of an owner** and **less of a sysadmin**? If not, it is probably the wrong move. Ownership is what produces the speed, so giving some of it up never buys velocity.

## Job 1: Setting up a new Base

The user has no Base yet, or wants another one.

**Follow the walkthrough in [README.md](README.md#setting-up-a-base)** — it is written to be executed top to bottom. The shape of it: `ownbasectl keygen` produces a public key, you ask the user to create an Ubuntu server with that key pasted in, they give you the IP, and `ownbasectl create --remote root@<ip> --wait` does everything else unattended.

Two things to get right:

- **Creating the machine is the user's step.** Providers need a human with a credit card. Give them the key, the OS version, and the size ([sizing](README.md#how-big-a-machine)); ask for the IP.
- **The owner key and the deploy key are different things.** `keygen` makes the key *you* use to reach the Base. `ssh-key add` makes the key the *Base* uses to clone git repos read-only. Both are in [INSTALL.md](INSTALL.md#two-different-ssh-keys).

| Need | Doc |
|---|---|
| Install reference, unusual servers, exit codes | [INSTALL.md](INSTALL.md) |
| Setup failed | [docs/troubleshooting.md](docs/troubleshooting.md) |

## Job 2: Operating a Base

You have CLI access to a running Base and are asked to change what is deployed, diagnose a problem, or check health.

**Start with [docs/operating.md](docs/operating.md)** for the order of operations. The rule that matters most: `ownbase.yaml` lives in an **external Git repo the user owns**, and the Base only ever reads it. Every change is committed client-side by `ownbasectl` and the Base is then told to reconcile. Never hand-edit anything on the Base, and never write to `runtime/`.

| Need | Doc |
|---|---|
| What's deployed on *this* Base | `ownbase.yaml` in its config repo; `ownbasectl status` for live state |
| `ownbase.yaml` schema, `ref:` updates, secrets | [docs/ownbase-yaml.md](docs/ownbase-yaml.md) |
| CLI command reference | [docs/cli.md](docs/cli.md) |
| Daemon HTTP API | [docs/api.md](docs/api.md) |
| Adding any service (the black-box contract) | [docs/integration-contract.md](docs/integration-contract.md) |
| Something failed | [docs/troubleshooting.md](docs/troubleshooting.md) |
| Removing OwnBase / exporting everything | [docs/uninstall.md](docs/uninstall.md) |

## Job 3: Modifying the OwnBase code itself

You are changing `cmd/ownbased`, `cmd/ownbasectl`, or `internal/` — the Go source that becomes the daemon and the CLI.

**Start with [docs/development.md](docs/development.md)** — build/test workflow (Tier-1 anywhere, Tier-2 on the Ubuntu VM), the invariants to preserve (idempotency, deterministic compiler, single writer to `runtime/`, taxonomy-audited actions, no plaintext secrets on disk, honest dry-runs), and the merge gate.

| Need | Doc |
|---|---|
| Why the code is the way it is (locked choices) | [docs/decisions.md](docs/decisions.md) — check before "fixing" anything that looks wrong |
| The durable rules of how a Base works | [docs/foundation/](docs/foundation/) — read once, in order |
| Canonical term definitions | [docs/foundation/lexicon.md](docs/foundation/lexicon.md) |
| Install / fresh-install verification | [INSTALL.md](INSTALL.md) |

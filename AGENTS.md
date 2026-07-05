# AGENTS.md

> Guide for AI agents working with OwnBase — either **operating** a user's Base, or **modifying the OwnBase code itself** (the daemon and `ownbasectl`).

## One sentence

> AI makes it easy to build software. OwnBase makes it easy to own it.

OwnBase turns a user-controlled Ubuntu machine (their **Base**) into a secure, self-maintaining home for AI-built services. The daemon and its stewardship are the product; the user owns everything it touches.

## Hard constraints

Do not violate these without the user's explicit direction.

| Constraint | Detail |
|---|---|
| User owns everything | Code, server, data, config, secrets, backups, domains. Never trap a user. |
| Nothing is mysterious | Plain files, Git as source of truth, human- and AI-readable layouts. No black boxes. |
| Operations disappear | If a user must learn Linux, Docker, nginx, or certs because of this system, it failed. |
| Every service is ownable | Removable, forkable, replaceable, data accessible, runs without any OwnBase-operated cloud. |
| Boring technology wins | Ubuntu, Podman, Postgres, Git, Caddy. Never Kubernetes. |
| No pre-built application images | Every service is built locally from source at a pinned `ref:`. |

When in doubt: does the change make the user **more of an owner** and **less of a sysadmin**? If not, it is probably the wrong change. See [docs/foundation/](docs/foundation/) for the full reasoning behind each constraint, and [docs/decisions.md](docs/decisions.md) for locked implementation choices — check both before "fixing" something that looks wrong.

---

## Job 1: Operating a Base

You have SSH/CLI access to a running Base and are asked to change what's deployed on it, diagnose a problem, or check its health.

1. **Read `OWNBASE.md`** in the Base's config repo first — it's generated fresh after every reconcile and lists every service, what it provides, what it requires, and how to reach it.
2. **The only way to change what's running is `ownbase.yaml` + a commit.** Never `podman run`, `systemctl edit`, or hand-edit anything under `runtime/` — those are compiler output and get overwritten on the next reconcile. See [docs/ownbase-yaml.md](docs/ownbase-yaml.md) for the schema.
3. **Push to the Base's own Forgejo**, not GitHub. The Base's Git host is local; commit and push there and the daemon reconciles automatically (hook-triggered, seconds not minutes).
4. **Use `ownbasectl` for everything else** — status, secrets, backups, security, updates. See [docs/cli.md](docs/cli.md) for the full command reference and [docs/api.md](docs/api.md) if you need to call the daemon's HTTP API directly.
5. **Updating a service** = edit `ref:` in `ownbase.yaml` and commit. There is no other update mechanism — the daemon never opens PRs or mutates the repo on its own initiative (except resolving a blank `ref:` to a concrete commit SHA, which it commits back transparently).
6. **Before anything destructive** (restore, delete), check `ownbasectl backup status <base>` — the guarantee only holds if the last verified restore actually passed.

## Job 2: Modifying the OwnBase code itself

You are changing `cmd/ownbased`, `cmd/ownbasectl`, or `internal/` — the Go source that becomes the daemon and the CLI.

### Before you start

Read [docs/foundation/](docs/foundation/) once, in order (`README.md` → `lexicon.md` → the rest) — it's short and defines the constraints every change must satisfy. Check [docs/decisions.md](docs/decisions.md) before changing anything that looks like an odd workaround; it's very likely intentional and the reason is recorded there.

### Building and testing

```bash
go build ./...
go test ./...                    # Tier-1 — runs anywhere, no VM, must be green before any commit
go vet ./...
golangci-lint run ./...
```

Tier-2 (integration) tests require an Ubuntu VM (Multipass) and exercise real Podman/systemd/Forgejo behavior that can't be faked on macOS:

1. Confirm the VM is running: `multipass list`
2. Sync changed files: `make sync-vm`
3. Build on the VM: `multipass exec ownbase-test -- bash -c "cd ~/ownbase && /usr/local/go/bin/go build -tags integration ./..."`
4. Run: `make test-vm`

### Key patterns to preserve

- **Idempotency.** Every reconcile/install/hardening step must be safe to run twice — check before acting, not "run once and hope."
- **Pure, deterministic compiler.** `internal/compiler` must produce byte-identical output for the same input, every time. Never let it depend on wall-clock time, randomness, or network state.
- **Single writer to `runtime/`.** Only the compiler writes there. Anything else touching those files is a bug.
- **Audit everything.** Every daemon action goes through `internal/schema` taxonomy (`NewAction`) and gets logged. An action type not in the taxonomy cannot execute — extend the taxonomy deliberately, don't work around it.
- **Plaintext secrets never touch disk.** Decrypt in memory, inject at container start, nothing else.
- **Dry-run everywhere it matters.** `plan`/`apply --dry-run` must be side-effect-free previews of the real path, not a separate implementation.

### Merge gate

All changes must keep `go test ./...` and `golangci-lint run ./...` green. Breaking a hard constraint above requires the user's explicit sign-off first, not a workaround.

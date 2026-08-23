# Developing OwnBase

> How to change the OwnBase source itself — `cmd/ownbased`, `cmd/ownbasectl`, and `internal/`. Covers the build/test workflow, the invariants every change must preserve, and the merge gate.

## Before you start

Read [foundation/](foundation/) once, in order (`README.md` → `lexicon.md` → the rest) — it's short and defines the constraints every change must satisfy. Check [decisions.md](decisions.md) before changing anything that looks like an odd workaround; it is very likely intentional and the reason is recorded there.

## Building and testing

Tier-1 tests run anywhere, with no VM, and must be green before any commit:

```bash
go build ./...
go test ./...
go vet ./...
golangci-lint run ./...
```

Tier-2 (integration) tests require an Ubuntu VM (Multipass) and exercise real Podman/systemd behavior that can't be faked on macOS:

1. Confirm the VM is running: `multipass list`
2. Sync changed files: `make sync-vm`
3. Build on the VM: `multipass exec ownbase-test -- bash -c "cd ~/ownbase && /usr/local/go/bin/go build -tags integration ./..."`
4. Run: `make test-vm`

The [desktop app](../desktop/README.md) is also Tier-1 — no window is opened, so it runs anywhere:

```bash
make app-check       # tsc, eslint, vitest, cargo fmt, cargo clippy
make app-e2e         # Playwright hermetic e2e (mocked Tauri IPC, no Multipass)
make app             # the app against a dev server, for looking at it
```

What that guards is the seam: every screen renders `ownbasectl --json`, so a field that moved on the Go side has to fail there rather than in a shipped bundle. If you change the shape of any `--json` output, run it.

The [marketing site](../site/README.md) is Tier-1 too — a static Astro build, no browser opened:

```bash
make site-check      # astro check + production build
make site            # dev server on :4321
```

Design tokens are shared with the desktop app via `shared/theme/preset.mjs`. A token change needs both `make site-check` and `make app-check`.

### Desktop e2e (Playwright)

The app is a thin window over `ownbasectl`. Tier-A e2e tests drive the React UI in Chromium with the three Tauri IPC commands (`cli_run` / `cli_stream` / `cli_cancel`) mocked in the browser — see `desktop/e2e/`. No native window, no Base, runs in CI on every push.

```bash
cd desktop && npm run e2e          # or: make app-e2e
cd desktop && npm run e2e:ui       # interactive Playwright UI
cd desktop && npm run e2e:capture -- <base>   # refresh golden --json from a live Base
```

**Release checklist — refresh captured CLI JSON.** Both `desktop/src/lib/types.ts` and `desktop/e2e/fixtures/data.ts` are hand-written from the Go structs. A field renamed on the Go side can pass typecheck and hermetic e2e until the golden files move. Before cutting a release (or after any `--json` shape change):

1. Provision a throwaway Base (`make smoke-test`), unlock the vault.
2. `cd desktop && npm run e2e:capture -- ownbase-fresh` — writes redacted docs under `e2e/fixtures/captured/`.
3. Review the redaction (no real IPs, hostnames, repo URLs, or key material).
4. `npm run test` — `captured-shape.test.ts` fails on missing required fields **and** unknown top-level keys.
5. Commit the refreshed goldens with the shape change.

Tier-B (local only, not CI) forwards the same IPC surface to a tiny bridge that spawns a real `ownbasectl` against an isolated `HOME`. Optional Multipass coverage sits next to `make smoke-test`:

```bash
npm run sidecar
make app-e2e-real                  # vault create via real CLI; extend for Multipass
```

A cloud VPS is not part of the automated matrix: after create, VM and remote Bases share the same SSH-tunnel code path, and the Multipass VM already exercises install + operate.

`desktop/go.mod` exists only to keep `go test ./...` out of `node_modules`, which ships a stray Go package. There is no first-party Go code under `desktop/`.

## Invariants to preserve

- **Idempotency.** Every reconcile/install/hardening step must be safe to run twice — check before acting, not "run once and hope."
- **Pure, deterministic compiler.** `internal/compiler` must produce byte-identical output for the same input, every time. Never let it depend on wall-clock time, randomness, or network state.
- **Single writer to `runtime/`.** Only the compiler writes there. Anything else touching those files is a bug.
- **Audit everything, with a principal.** Every daemon action goes through the `internal/schema` taxonomy (`NewAction`), carries an acting principal (`WithPrincipal`), and is logged. An action type not in the taxonomy cannot execute — extend the taxonomy deliberately, don't work around it.
- **Default-deny on the daemon API.** A new HTTP route is owner-only unless `authz.RouteAccess` maps it. Owner-only routes refuse service principals even when granted `*`. Tests: `internal/authz/route_test.go`.
- **The Base never pushes the tracked ref.** Only `refs/heads/ownbase/agent/*`, force-with-lease. Merge stays on the forge.
- **The vault is the config-source pin.** No client path may overwrite a non-empty `ConfigRepoURL` from Base-reported state. Mismatch warns.
- **No ambient SSH config on the Base.** `GIT_SSH_COMMAND` uses the single managed key; a `config` file is deleted, never honored.
- **Mutable refs always refetch.** Only full 40/64-char commit SHAs may short-circuit a fetch.
- **Socket dirs before containers.** Bind-mount sources for `ownbase_access` must exist before Podman apply, and must be **directories** (stable inode across socket replacement).
- **Plaintext secrets never touch disk.** Decrypt in memory, inject at container start, nothing else.
- **Dry-run everywhere it matters.** `plan`/`apply --dry-run` must be side-effect-free previews of the real path, not a separate implementation. In particular, `prepareAccess` / `SocketManager.Sync` must not run under dry-run (it opens listeners and can close sockets for services absent from a previewed config).
- **Private keys stay in the agent.** A key from the [vault](vault.md) is used by asking the agent for a signature. Nothing writes one to disk or passes one around as bytes, and no password is ever accepted in argv — `ps` can read argv for as long as the process lives.
- **Every save of the vault re-seeds.** KDBX4 is ChaCha20; writing twice under the same key and nonce reuses a keystream. `internal/vault` re-randomizes on every save and merges against the file on disk rather than overwriting it, because the vault is expected to be in a synced folder and open in KeePassXC at the same time.
- **Sessions are always recorded.** `ownbasectl ssh` has no flag that turns recording off, and adding one would make the audit trail worth nothing. Both directions are captured; output alone cannot show a command that produced none.
- **One control plane.** `ownbasectl` owns the semantics — the vault, the agent, the git commits, the reconcile. The desktop app spawns it and renders the result; it must never grow a second implementation of any of that, or "what is deployed on this Base" starts having two answers that can disagree. If the app needs something, the answer is a new `--json` flag on the CLI. The in-container socket API is a second *consumer* of the daemon, not a second control plane — same mux, same taxonomy.

## Merge gate

Breaking a hard constraint (see [MISSION.md](../MISSION.md)) requires the user's explicit sign-off first, not a workaround.

**Every change lands through a pull request.** Do not push commits straight to `main`. Branch, open a PR, wait for the gate below, then merge. Releases are tagged from `main` only after that. (Same rule is in [AGENTS.md](../AGENTS.md) so agents cannot miss it.)

| Layer | Requirement |
|---|---|
| **Local** | Always: `go test ./...` and `golangci-lint run ./...`. If you touched `desktop/` or any `--json` shape: `make app-check` **and** `make app-e2e`. (`app-check` alone does not run Playwright.) If you touched `site/` or `shared/`: `make site-check` (and `make app-check` too when the shared theme changed). |
| **CI on the PR** | Tier 1 · Desktop app (typecheck, lint, vitest, **Playwright e2e**, build, clippy) · Site (check + build) · Tier 2 · Tier 2 root — all green |
| **Bugbot** | Every Cursor Bugbot finding is **fixed, or replied to with rationale**. Never silently dismiss. |
| **Review** | User approved, or they explicitly tell you to merge |

### Testing conventions (desktop)

The desktop suite is self-enforcing in several places. Extend it when you change the surface it covers — do not work around a failing gate.

| If you… | You must… | Enforced by |
|---|---|---|
| add an export to `api.ts` | add an argv test that calls `cover("name")` | `src/lib/api.test.ts` — "every exported api operation has an argv test" |
| add a UI flow that calls the CLI | add an e2e spec **and** extend `e2e/shim/install.ts` for the new argv | fall-through guard in `e2e/fixtures/test.ts` (fails if the UI shows "e2e mock has no handler") |
| add a confirm / destructive gate | assert **dismiss** (zero real mutations) **and** **accept** (exactly one) | review + existing confirm-gates / destructive specs |
| pass a secret through the CLI | assert it is absent from argv and present on stdin | `api.test.ts` invariants |
| change any `--json` shape | update `types.ts`, hand-written fixtures, and refresh `e2e/fixtures/captured/` together | `captured-shape.test.ts` (missing required fields **and** unknown top-level keys) |
| add or reshape a fixture in `data.ts` | keep it congruent with `types.ts` | `fixtures-shape.test.ts` |
| write a new Playwright spec under `e2e/tests/` | import `{ test, expect, waitForQuiet }` from `../fixtures/test` — never from `@playwright/test` | ESLint `no-restricted-imports` on `e2e/tests/**` |

Hermetic e2e is a CI gate (`.github/workflows/ci.yml` desktop job). Locally: `make app-e2e`. Tier-B (`make app-e2e-real`) is optional and not a PR gate.

## Verifying a fresh install end-to-end

This is for verifying the installer itself still works correctly after changing `install.sh`, the daemon's bootstrap path, or `internal/vmhost`. It is separate from the automated tiers above because the fresh-install path (pass zero → Quadlet bootstrap → reconcile loop) cannot be fully exercised by unit or integration tests; it requires a real installer run on a clean machine.

### Run it

```bash
go run ./cmd/ownbasectl create ownbase-fresh
# equivalent to: make smoke-test
```

`make smoke-test` and `make connect-vm` are thin aliases for this same command — the daemon binary is built fresh from this checkout every run, and the resulting profile is registered automatically, so there is no separate "connect" step. `create` always deletes any existing VM with the same name before launching, so re-running it is already "provision a clean VM" — no separate `multipass delete`/`launch` step needed.

### Watch the daemon

```bash
multipass shell ownbase-fresh
sudo journalctl -u ownbased -f
```

### What a successful install looks like

```
pass zero complete — host is hardened
bootstrap core: ...                      ← Quadlet units written, SIGHUP fired
starting (mode=integration ...)          ← real Podman+Quadlet mode
already converged — no changes needed
update detection enabled ...
```

### Verify after startup

```bash
# Get the VM IP
multipass info ownbase-fresh | grep IPv4

# Open a VM shell and check from inside
multipass exec ownbase-fresh -- sudo podman ps                         # caddy running
multipass exec ownbase-fresh -- sudo systemctl list-units 'ownbase-*'  # units loaded
multipass exec ownbase-fresh -- sudo ls /etc/containers/systemd/       # Quadlet unit files
multipass exec ownbase-fresh -- sudo ls /opt/ownbase/checkout /opt/ownbase/repos  # config checkout + service bare clones

# Verify trivy was installed by PassZero, and that it can actually scan —
# both are required for `security` to report real results instead of
# "scan failed" for every image.
multipass exec ownbase-fresh -- trivy --version
multipass exec ownbase-fresh -- systemctl is-active podman.socket
```

### Then use `ownbasectl` as usual

```bash
go run ./cmd/ownbasectl status ownbase-fresh
go run ./cmd/ownbasectl checkup ownbase-fresh
go run ./cmd/ownbasectl config get ownbase-fresh
```

## Local HTTPS via tunnel (`ownbasectl tunnel`)

`create` is guaranteed to never prompt on the remote path — it must stay safe for an AI agent to run unattended — so it carries no TLS logic of any kind. A fresh local VM therefore has no domain, no public ports, and no real HTTPS. To see a service over trusted HTTPS locally, add a `domain:`/`domains:` to it in `ownbase.yaml` and run:

```bash
go run ./cmd/ownbasectl tunnel ownbase-fresh
```

This is the one command allowed to prompt (a one-time `sudo mkcert -install`). It opens an SSH tunnel directly to the service's container port (bypassing Caddy) and serves it at `https://<domain>.localhost:8443`. See [ownbase-yaml.md](ownbase-yaml.md#local-https-via-tunnel-ownbasectl-tunnel) and [decisions.md](decisions.md#ssh-tunnel-bridge-ownbasectl-tunnel) for the full design. There is no code-sync — iterate by pushing to the service's git host and running `ownbasectl deploy`, exactly as in production.

## Agent-level bootstrap tests

These tests exercise `bootstrapCore` directly — the Quadlet installation, SIGHUP reload, and `systemctl start` path that the E2E tests in `internal/install/` do not cover. Run them on `ownbase-test` (not `ownbase-fresh`, which has a live daemon using the same container names).

```bash
# Sync the latest code first
make sync-vm VM=ownbase-test

# Run
multipass exec ownbase-test -- bash -c \
  'cd ~/ownbase && sudo /usr/local/go/bin/go test -tags=integration -count=1 \
   ./cmd/ownbased/... -v -timeout 10m'
```

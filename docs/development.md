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

## Invariants to preserve

- **Idempotency.** Every reconcile/install/hardening step must be safe to run twice — check before acting, not "run once and hope."
- **Pure, deterministic compiler.** `internal/compiler` must produce byte-identical output for the same input, every time. Never let it depend on wall-clock time, randomness, or network state.
- **Single writer to `runtime/`.** Only the compiler writes there. Anything else touching those files is a bug.
- **Audit everything.** Every daemon action goes through the `internal/schema` taxonomy (`NewAction`) and gets logged. An action type not in the taxonomy cannot execute — extend the taxonomy deliberately, don't work around it.
- **Plaintext secrets never touch disk.** Decrypt in memory, inject at container start, nothing else.
- **Dry-run everywhere it matters.** `plan`/`apply --dry-run` must be side-effect-free previews of the real path, not a separate implementation.

## Merge gate

All changes must keep `go test ./...` and `golangci-lint run ./...` green. Breaking a hard constraint (see [MISSION.md](../MISSION.md)) requires the user's explicit sign-off first, not a workaround.

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
multipass exec ownbase-fresh -- sudo ls /opt/ownbase/repo /opt/ownbase/repos  # config + service bare repos

# Verify trivy was installed by PassZero
multipass exec ownbase-fresh -- trivy --version
```

### Then use `ownbasectl` as usual

```bash
go run ./cmd/ownbasectl status ownbase-fresh
go run ./cmd/ownbasectl checkup ownbase-fresh
go run ./cmd/ownbasectl config get ownbase-fresh
```

## Local HTTPS via tunnel (`ownbasectl tunnel`)

`create`/`vm start|stop|restart` are guaranteed to never prompt for anything — they must stay safe for an AI agent to run unattended — so they carry no TLS logic of any kind. A fresh local VM therefore has no domain, no public ports, and no real HTTPS. To see a service over trusted HTTPS locally, add a `domain:`/`domains:` to it in `ownbase.yaml` and run:

```bash
go run ./cmd/ownbasectl tunnel ownbase-fresh
```

This is the one command allowed to prompt (a one-time `sudo mkcert -install`). It opens an SSH tunnel directly to the service's container port (bypassing Caddy) and serves it at `https://<domain>.localhost:8443`. See [ownbase-yaml.md](ownbase-yaml.md#local-https-during-development-ownbasectl-tunnel) and [decisions.md](decisions.md#ssh-tunnel-bridge-ownbasectl-tunnel) for the full design. There is no code-sync — iterate by pushing to the service's bare repo and bumping `ref:`, exactly as in production.

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

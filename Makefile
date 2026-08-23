.PHONY: all build build-linux test test-vm test-integration sync-vm smoke-test connect-vm lint fmt fmt-fix vet tidy clean \
        app app-build app-check app-sidecar app-e2e app-e2e-real \
        site site-build site-check

VM      ?= ownbase-test
VM_NAME ?= ownbase-fresh
GO_VM   := /usr/local/go/bin/go

# Default: format, vet, lint, test
all: fmt vet lint test

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

build:
	go build ./...

# Production Linux binary — must use -tags=integration (without it the daemon
# runs in no-op mode and no containers are started).
build-linux:
	GOOS=linux GOARCH=arm64 go build -tags=integration \
	  -o /tmp/ownbase-releases/latest/ownbased-linux-arm64 ./cmd/ownbased/

# ---------------------------------------------------------------------------
# Test
# ---------------------------------------------------------------------------

# Tier-1: run locally on macOS, must be green before every commit.
test:
	go test ./...

# Tier-2: run on the VM. Syncs nothing — run 'make sync-vm' first.
# Override the target VM with: make test-vm VM=ownbase-fresh
#
# Three passes:
#   1. Non-root: all packages (fast schema/logic tests; secwatch probes skip gracefully
#      on a fresh VM because ufw/fail2ban are not yet installed).
#   2. Root, sequential (-p 1): install (PassZero hardens the machine — installs ufw,
#      fail2ban, Podman) → secwatch (probes now find ufw/fail2ban, no skips) → agent.
#      -p 1 ensures install finishes before secwatch starts.
test-vm:
	multipass exec $(VM) -- bash -c \
	  'cd ~/ownbase && $(GO_VM) test -tags=integration -count=1 ./... -timeout 600s'
	multipass exec $(VM) -- bash -c \
	  'cd ~/ownbase && sudo $(GO_VM) test -tags=integration -count=1 -p 1 \
	   ./internal/install/... ./internal/secwatch/... ./cmd/ownbased/... -timeout 600s'

# Tier-2: run locally on a Linux host (CI or native Ubuntu). Requires root
# for the install/agent tests — run with sudo if needed.
test-integration:
	go test -tags=integration ./...

# Fresh install smoke test on a clean local VM. `ownbasectl create` is the
# Go-instrumented replacement for the old testing/smoke-install.sh: it
# provisions (or re-provisions) the VM, builds ownbased from this checkout,
# runs the installer, and registers the resulting profile — all in one step.
# Override: make smoke-test VM_NAME=my-vm CADDY_EMAIL=you@example.com
smoke-test:
	go run ./cmd/ownbasectl create $(VM_NAME) \
	  $(if $(CADDY_EMAIL),--caddy-email $(CADDY_EMAIL))

# Kept as an alias for smoke-test: `create` already registers the profile as
# part of provisioning, so there is no separate "connect" step anymore —
# this target just re-runs the same command idempotently.
# Override: make connect-vm VM_NAME=my-vm
connect-vm: smoke-test

# ---------------------------------------------------------------------------
# VM code sync
# ---------------------------------------------------------------------------
# Full repo sync to the VM (initial or full refresh). Excludes .git and any
# locally-compiled host-platform binaries that may exist at the repo root
# (produced by 'go build ./cmd/...'). The VM builds its own Linux binaries.
sync-vm:
	# Use git archive so only tracked source files are synced — compiled
	# binaries are gitignored and never included. BSD tar --exclude patterns
	# match any path component, so './ownbased' would accidentally strip
	# cmd/ownbased/ source as well; git archive sidesteps this entirely.
	git archive HEAD -o /tmp/ownbase-sync.tar
	multipass transfer /tmp/ownbase-sync.tar $(VM):/home/ubuntu/ownbase-sync.tar
	multipass exec $(VM) -- bash -c \
	  'mkdir -p ~/ownbase && tar xf ~/ownbase-sync.tar -C ~/ownbase \
	   && rm ~/ownbase-sync.tar'

# ---------------------------------------------------------------------------
# Desktop app (desktop/)
# ---------------------------------------------------------------------------
# The app bundles ownbasectl as a sidecar and does everything through it, so
# every target here rebuilds that binary first — otherwise the window talks to
# whatever CLI was compiled last, which looks like a bug in the app.

DESKTOP := desktop

app-sidecar:
	cd $(DESKTOP) && npm run sidecar

# Dev: Vite plus the native window.
app: app-sidecar
	cd $(DESKTOP) && npm run app

# Release bundle for this platform (.dmg/.app on macOS).
app-build: app-sidecar
	cd $(DESKTOP) && npm run app:build

# Tier-1 for the app: types, lint, unit tests, and the Rust bridge. No window is
# opened, so this runs anywhere — it is the same set the CI desktop job runs, and
# the two should be changed together.
app-check:
	cd $(DESKTOP) && npm run typecheck && npm run lint && npm run test
	cd $(DESKTOP)/src-tauri && cargo fmt --check && cargo clippy --all-targets -- -D warnings

# Hermetic Playwright e2e (Tier A): Vite + mocked Tauri IPC. No Multipass, no
# window. Safe for CI and for a quick local check before a PR.
app-e2e:
	cd $(DESKTOP) && npx playwright install chromium && npm run e2e

# Real-CLI Playwright e2e (Tier B): spawns ownbasectl against a temp vault and
# (optionally) a Multipass Base. Local only — not wired into CI.
app-e2e-real:
	cd $(DESKTOP) && npm run e2e:real

# ---------------------------------------------------------------------------
# Marketing site (site/)
# ---------------------------------------------------------------------------
# Static Astro build for ownbase.ai. Shares design tokens with the desktop
# app via shared/theme/preset.mjs — a token change touches both.

SITE := site

site:
	cd $(SITE) && npm run dev

site-build:
	cd $(SITE) && npm run build

# What the CI site job runs: typecheck + production build.
site-check:
	cd $(SITE) && npm run build

# ---------------------------------------------------------------------------
# Code quality
# ---------------------------------------------------------------------------

lint:
	golangci-lint run ./...

fmt:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
	  echo "The following files need gofmt:"; \
	  echo "$$unformatted"; \
	  exit 1; \
	fi

fmt-fix:
	gofmt -w .

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	go clean ./...

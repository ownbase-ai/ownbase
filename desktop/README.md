# The OwnBase app

> A window onto `ownbasectl`. Set up a Base with a guided wizard, watch the health of the ones you have, and replay every session that was ever opened on them.

User-facing documentation is [docs/desktop.md](../docs/desktop.md). This file is for working on the app.

## The one architectural rule

**The app does not know how to do anything. It runs `ownbasectl` and renders what came back.**

There is no Rust code here that understands the vault, SSH, KDBX, or `ownbase.yaml`, and there never should be. `ownbasectl` already owns those semantics; a second implementation would be a second control plane, and "what is actually deployed on this Base" would start having two answers that could disagree. Config changes stay what they are everywhere else in OwnBase: a client-side git commit followed by a reconcile.

Concretely:

| Layer | Job | Where |
|---|---|---|
| React | render JSON, collect input | `src/` |
| `src/lib/api.ts` | name the operations, own the argv | one file, so a flag rename is one edit |
| `src/lib/cli.ts` | invoke the bridge, parse, classify exit codes | — |
| Rust | spawn the sidecar, forward bytes | `src-tauri/src/cli.rs` |
| `ownbasectl` | everything real | `../cmd/ownbasectl` |

If you find yourself wanting to add a Rust dependency that parses something, the answer is almost always a new `--json` flag on the CLI instead.

## Prerequisites

- Node 20+ and Rust 1.77+
- Go 1.24+, to build the bundled CLI
- Tauri's platform dependencies ([prerequisites](https://tauri.app/start/prerequisites/)) — on macOS that is just Xcode command line tools

## Running it

```bash
npm install
npm run sidecar     # builds ownbasectl into src-tauri/binaries/
npm run app         # tauri dev: Vite on :5273 + the native window
```

`npm run sidecar` is not automatic. It shells out to `go build`, and making every `tauri dev` pay for that would be annoying — but **re-run it whenever you change the CLI**, or the app will keep talking to the binary you built last time.

Other scripts:

```bash
npm run typecheck   # tsc --noEmit
npm run build       # typecheck + production frontend bundle
npm run app:build   # full platform bundle (.dmg/.app on macOS)
npm run e2e         # Playwright hermetic e2e (mocked IPC, CI)
npm run e2e:ui      # same, with the Playwright UI
npm run e2e:real    # real ownbasectl via local bridge (dev machine)
npm run e2e:capture -- <base>  # refresh redacted goldens in e2e/fixtures/captured/
```

## End-to-end tests

The app never talks to a Base directly — only through the sidecar — so Playwright runs the Vite UI in a normal browser and injects a mock of `__TAURI_INTERNALS__` (`e2e/shim/install.ts`). Scenarios hand the mock canned `--json` documents from `e2e/fixtures/data.ts`. That is enough to cover unlock, the setup wizard, dashboard tabs, the dry-run→confirm service gate, sessions, and vault screens without Multipass or a VPS.

`e2e/fixtures/captured/` holds redacted live CLI output. `src/lib/captured-shape.test.ts` decodes those goldens against `types.ts` and fails on unknown top-level keys — the rename signal hermetic fixtures alone cannot catch. Refresh with `npm run e2e:capture -- <base>` after any `--json` shape change (see the release checklist in [docs/development.md](../docs/development.md#desktop-e2e-playwright)).

For a real CLI path, `e2e/bridge/server.mjs` spawns `ownbasectl` under an isolated `HOME`; `npm run e2e:real` drives it. See [docs/development.md](../docs/development.md#desktop-e2e-playwright).

## The sidecar

`ownbasectl` is bundled rather than assumed to be installed, because the app is useless without it and version drift between the two would show up as missing fields rather than as a clear error.

Tauri finds a sidecar by target triple: `src-tauri/binaries/ownbasectl-aarch64-apple-darwin`. `scripts/build-sidecar.mjs` reads the host triple from `rustc -vV`, maps it to `GOOS`/`GOARCH`, and stamps the same version string a release build would. Cross-compile with `OWNBASE_SIDECAR_TARGET=x86_64-apple-darwin npm run sidecar`.

## What the webview is allowed to run

`src-tauri/src/cli.rs` holds an allowlist of `ownbasectl` subcommands, and the shell plugin's own `execute`/`spawn` permissions are deliberately **not** granted in `capabilities/default.json`. So the webview cannot name a program to run — it can only ask for one of the listed subcommands.

This matters because the app renders things that came off a Base: session transcripts, service names, daemon output. If any of that ever managed to execute script, an unrestricted bridge would let it run arbitrary commands as the user on every machine they own.

`ssh` and `tunnel` are not on the list. Both take an arbitrary command or hold an interactive session open, which is the exact shape the list exists to exclude. The app reads *recordings* of sessions; it never opens one.

## The icon

Brand mark lives at `src-tauri/icon-mark.png`. Rebuild the 1024² intermediate (white rounded tile + centered mark, so the dock reads it like other app icons), then derive every platform size into `src-tauri/icons/` (those are committed; `icon-source.png` is gitignored):

```sh
node scripts/make-icon.mjs          # needs ImageMagick (`magick` on PATH)
npx tauri icon src-tauri/icon-source.png
```

Commit `icon-mark.png` and `src-tauri/icons/`.

## Conventions

- **Tailwind, no component library.** The app is mostly dense status information, and the primitives in `src/components/ui.tsx` are enough for it.
- **`unknown` is a health state.** `Tone` has `unknown` so "we have not checked" never renders the same as "fine".
- **Exit codes are information.** `CliError.code` carries the CLI's classification; 7 means the vault is locked and sends the user to the unlock screen, 3 means preflight failed and has a specific explanation. Do not collapse them into one error box.
- **Passwords go over stdin.** Never in argv — `ps` can read argv for as long as the process lives. `api.ts` already does this; keep it that way.
- **Specs import `test` from `e2e/fixtures/test`.** That is what arms the shim fall-through guard. Importing `@playwright/test` directly in `e2e/tests/` is an ESLint error.
- **Use the shared `waitForQuiet`.** Do not copy a local polling loop into a new spec; import it from `fixtures/test`.
- **Extend the shim, do not ignore fall-through.** A new CLI path the UI calls needs a handler (or a `fails` / `streams` rule) in `e2e/shim/install.ts`. The post-test guard fails the suite if "e2e mock has no handler" appears in the page.
- **Confirm gates assert both halves.** Dismiss → zero real mutations; accept → exactly one. Secrets stay off argv.

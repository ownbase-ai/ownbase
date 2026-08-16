// Installs a browser-side mock of Tauri's IPC surface so Playwright can drive
// the React app without a native window. The app only talks to three commands
// (cli_run, cli_stream, cli_cancel) plus plugin:dialog|open — see src/lib/cli.ts.
//
// Scenario state is serializable JSON passed into addInitScript; the mock keeps
// mutable vault/list state in window.__E2E__ so unlock/create flows work.

import type { Page } from "@playwright/test";

import type {
  BaseSummary,
  Checkup,
  ConfigPreview,
  KeygenResult,
  SessionMeta,
  VaultStatus,
} from "../../src/lib/types";

export type StreamEvent =
  | { kind: "stdout"; line: string }
  | { kind: "stderr"; line: string }
  | { kind: "finished"; code: number }
  | { kind: "failed"; message: string };

export type CliCall = {
  cmd: "cli_run" | "cli_stream" | "cli_cancel" | "plugin:dialog|open";
  args?: string[];
  stdin?: string | null;
  id?: string;
};

export type FailRule = {
  /** Args must contain these tokens in order (ignoring --json / --yes / --wait). */
  match: string[];
  code: number;
  stderr: string;
  stdout?: string;
};

export type StreamRule = {
  match: string[];
  events: StreamEvent[];
  /** Artificial delay between events (ms). Default 0. */
  delayMs?: number;
};

export type Scenario = {
  /**
   * Initial vault phase.
   * - absent: no vault_path (first launch)
   * - locked: vault exists, needs password
   * - unlocked: app lands in the shell
   * - broken: invoke throws (CLI cannot run)
   */
  vault?: "absent" | "locked" | "unlocked" | "broken";
  vaultPath?: string;
  /** Password that unlock / init accept. Wrong passwords return exit 1. */
  password?: string;
  bases?: BaseSummary[];
  /** Checkup document keyed by Base name. */
  checkup?: Record<string, Checkup>;
  /**
   * Optional sequence of checkup documents per Base. Each call shifts the next
   * document; after exhaustion falls back to `checkup[name]`.
   */
  checkupSequence?: Record<string, Checkup[]>;
  sessions?: SessionMeta[];
  sessionCast?: Record<string, string>;
  sessionTranscript?: Record<string, string>;
  keygen?: KeygenResult;
  servicePreview?: ConfigPreview;
  deployPreview?: ConfigPreview;
  upgradeCheck?: unknown;
  /** Override for `version --check --json` (default is a dev CLI, no badge). */
  versionCheck?: unknown;
  dbStatus?: unknown;
  dbRestore?: unknown;
  recoveryKit?: unknown;
  configYaml?: string;
  /** Dry-run body for `backup setup --dry-run`. */
  backupSetupPreview?: ConfigPreview;
  fails?: FailRule[];
  streams?: StreamRule[];
  /** Path returned by the folder picker (plugin:dialog|open). */
  dialogPath?: string;
  /** stderr body for backup rekey --generate (includes generated password line). */
  rekeyStderr?: string;
};

declare global {
  interface Window {
    __E2E__?: {
      calls: CliCall[];
      scenario: Scenario;
      vault: VaultStatus;
      bases: BaseSummary[];
      clearFails: () => void;
    };
  }
}

/**
 * Inject the Tauri IPC mock and open the app. Call once per test before any
 * interaction. Reloading the page re-runs the init script with the same scenario.
 */
export async function openApp(page: Page, scenario: Scenario = {}): Promise<void> {
  await page.addInitScript(installMock, scenario);
  await page.goto("/");
}

/** Every cli_run / cli_stream / dialog call the mock has seen. */
export async function getCalls(page: Page): Promise<CliCall[]> {
  return page.evaluate(() => window.__E2E__?.calls ?? []);
}

/**
 * True when a recorded call's non-flag argv starts with `match` (anchored
 * prefix). Same semantics as the shim's own routing matcher — no subsequence
 * fallback, no scanning mid-argv. Optional `cmd` filters cli_run vs cli_stream.
 */
export function callMatched(
  calls: CliCall[],
  match: string[],
  opts?: { cmd?: CliCall["cmd"]; includeFlags?: string[] },
): boolean {
  return calls.some((c) => {
    if (opts?.cmd && c.cmd !== opts.cmd) return false;
    if (!c.args) return false;
    if (!argsPrefixed(c.args, match)) return false;
    if (opts?.includeFlags) {
      for (const f of opts.includeFlags) {
        if (!c.args.includes(f)) return false;
      }
    }
    return true;
  });
}

/** Non-flag tokens from an argv (flags and their values stay out). */
export function nonFlagTokens(args: string[]): string[] {
  const out: string[] = [];
  for (let i = 0; i < args.length; i++) {
    const a = args[i]!;
    if (a.startsWith("--")) {
      // Skip a following value unless the next token is also a flag / absent.
      const next = args[i + 1];
      if (next !== undefined && !next.startsWith("-") && !a.includes("=")) i++;
      continue;
    }
    if (a.startsWith("-") && a.length === 2) {
      const next = args[i + 1];
      if (next !== undefined && !next.startsWith("-")) i++;
      continue;
    }
    out.push(a);
  }
  return out;
}

/** True when `match` is an anchored prefix of the non-flag tokens in `args`. */
export function argsPrefixed(args: string[], match: string[]): boolean {
  const tokens = nonFlagTokens(args);
  if (tokens.length < match.length) return false;
  for (let i = 0; i < match.length; i++) {
    if (tokens[i] !== match[i]) return false;
  }
  return true;
}

/** Calls whose non-flag argv is prefixed by `match` and that are not dry-runs. */
export function realMutations(
  calls: CliCall[],
  match: string[],
  opts?: { cmd?: CliCall["cmd"] },
): CliCall[] {
  return calls.filter(
    (c) =>
      c.args &&
      (!opts?.cmd || c.cmd === opts.cmd) &&
      argsPrefixed(c.args, match) &&
      !c.args.includes("--dry-run"),
  );
}

// ---------------------------------------------------------------------------
// Runs inside the browser. Keep this self-contained: no imports, no closures
// over Node values other than the serialized `scenario` argument.
// ---------------------------------------------------------------------------

function installMock(scenario: Scenario): void {
  type SE =
    | { kind: "stdout"; line: string }
    | { kind: "stderr"; line: string }
    | { kind: "finished"; code: number }
    | { kind: "failed"; message: string };

  type CliResult = { code: number; stdout: string; stderr: string };
  type VS = {
    running: boolean;
    unlocked: boolean;
    vault_path?: string;
    bases: number;
    keys: number;
    unlocked_at?: string;
    idle_timeout_seconds: number;
    locks_at?: string;
    pid: number;
    ssh_agent_socket?: string;
    version?: string;
  };
  type BS = {
    name: string;
    host?: string;
    kind: "remote" | "vm" | "key-only" | "unregistered-vm";
    vm_state?: string;
    registered: boolean;
    ssh_user?: string;
    ssh_port?: number;
    has_token: boolean;
    has_key: boolean;
    config_repo_url?: string;
    config_ref?: string;
  };

  const password = scenario.password ?? "test-master-password";
  const vaultPath = scenario.vaultPath ?? "/tmp/ownbase-e2e/vault.kdbx";
  const phase = scenario.vault ?? "unlocked";

  function baseVault(unlocked: boolean): VS {
    const bases = (scenario.bases ?? []).length;
    const keys = (scenario.bases ?? []).filter((b) => b.has_key).length || bases;
    if (phase === "absent" && !unlocked) {
      return {
        running: false,
        unlocked: false,
        bases: 0,
        keys: 0,
        idle_timeout_seconds: 0,
        pid: 0,
      };
    }
    return {
      running: true,
      unlocked,
      vault_path: vaultPath,
      bases,
      keys,
      idle_timeout_seconds: 3600,
      pid: 4242,
      unlocked_at: unlocked ? "2026-08-15T12:00:00Z" : undefined,
      locks_at: unlocked ? "2026-08-15T13:00:00Z" : undefined,
      ssh_agent_socket: "/tmp/ownbase-e2e/agent.sock",
      version: "0.1.0-e2e",
    };
  }

  const checkupQueues: Record<string, unknown[]> = {};
  for (const [name, docs] of Object.entries(scenario.checkupSequence ?? {})) {
    checkupQueues[name] = [...docs];
  }

  const state = {
    vault: baseVault(phase === "unlocked") as VS,
    bases: [...(scenario.bases ?? [])] as BS[],
    calls: [] as Array<{
      cmd: string;
      args?: string[];
      stdin?: string | null;
      id?: string;
    }>,
    streams: new Map<string, { cancelled: boolean }>(),
    secretKeys: ["DATABASE_URL", "API_TOKEN"] as string[],
    fails: [...(scenario.fails ?? [])] as Array<{
      match: string[];
      code: number;
      stderr: string;
      stdout?: string;
    }>,
  };

  const e2e = {
    get calls() {
      return state.calls;
    },
    scenario,
    get vault() {
      return state.vault;
    },
    get bases() {
      return state.bases;
    },
    /** Tests call this before retrying a path that previously failed. */
    clearFails() {
      state.fails = [];
    },
  };
  (window as unknown as { __E2E__: typeof e2e }).__E2E__ = e2e;

  const callbacks = new Map<number, (data: unknown) => void>();

  function transformCallback(callback?: (data: unknown) => void, once = false): number {
    const id = window.crypto.getRandomValues(new Uint32Array(1))[0]!;
    callbacks.set(id, (data) => {
      if (once) callbacks.delete(id);
      callback?.(data);
    });
    return id;
  }

  function unregisterCallback(id: number): void {
    callbacks.delete(id);
  }

  function runCallback(id: number, data: unknown): void {
    const cb = callbacks.get(id);
    if (cb) cb(data);
  }

  function channelId(onEvent: unknown): number | null {
    if (onEvent && typeof onEvent === "object" && "id" in onEvent) {
      return (onEvent as { id: number }).id;
    }
    if (typeof onEvent === "string" && onEvent.startsWith("__CHANNEL__:")) {
      return Number(onEvent.slice("__CHANNEL__:".length));
    }
    if (typeof onEvent === "number") return onEvent;
    return null;
  }

  function stripMeta(args: string[]): string[] {
    return args.filter(
      (a) =>
        a !== "--json" &&
        a !== "--yes" &&
        a !== "--wait" &&
        a !== "--password-stdin" &&
        a !== "--creds-stdin",
    );
  }

  /** True when `pattern` is a prefix of the subcommand tokens (flags stripped). */
  function matches(args: string[], pattern: string[]): boolean {
    const clean = stripMeta(args);
    if (clean.length < pattern.length) return false;
    for (let i = 0; i < pattern.length; i++) {
      if (clean[i] !== pattern[i]) return false;
    }
    return true;
  }

  function ok(body: unknown): CliResult {
    return {
      code: 0,
      stdout: typeof body === "string" ? body : JSON.stringify(body, null, 2) + "\n",
      stderr: "",
    };
  }

  function err(code: number, stderr: string, stdout = ""): CliResult {
    return {
      code,
      stdout,
      stderr: stderr.endsWith("\n") ? stderr : stderr + "\n",
    };
  }

  function findFail(args: string[]) {
    return state.fails.find((f) => matches(args, f.match));
  }

  function findStream(args: string[]) {
    return (scenario.streams ?? []).find((s) => matches(args, s.match));
  }

  async function handleRun(args: string[], stdin: string | null): Promise<CliResult> {
    const fail = findFail(args);
    if (fail) return err(fail.code, fail.stderr, fail.stdout ?? "");

    if (matches(args, ["vault", "status"])) {
      return ok(state.vault);
    }

    if (matches(args, ["vault", "init"])) {
      const initIdx = args.indexOf("init");
      const path = args[initIdx + 1] && !args[initIdx + 1]!.startsWith("-")
        ? args[initIdx + 1]!
        : vaultPath;
      if (stdin == null || stdin === "") {
        return err(2, "Error: password required on stdin");
      }
      state.vault = {
        ...baseVault(true),
        vault_path: path,
        unlocked: true,
        bases: 0,
        keys: 0,
      };
      state.bases = [];
      return ok({
        vault_path: path,
        created: true,
        unlocked: true,
        status: state.vault,
      });
    }

    if (matches(args, ["vault", "unlock"])) {
      if (stdin !== password) {
        return err(1, "Error: wrong master password");
      }
      state.vault = baseVault(true);
      return ok(state.vault);
    }

    if (matches(args, ["vault", "lock"])) {
      state.vault = { ...baseVault(false), vault_path: vaultPath, unlocked: false };
      return ok("");
    }

    if (matches(args, ["vault", "passwd"])) {
      if (stdin == null || stdin === "") {
        return err(2, "Error: password required on stdin");
      }
      return ok("");
    }

    if (matches(args, ["vault", "open"])) {
      if (stdin !== password) {
        return err(1, "Error: wrong master password");
      }
      state.vault = baseVault(true);
      return ok(state.vault);
    }

    if (matches(args, ["vault", "recovery-string"])) {
      return ok({
        recovery_string: "ownbase-recovery-v1:e2e-fixture",
        location: "remote",
      });
    }

    if (matches(args, ["agent", "stop"])) {
      state.vault = { ...state.vault, running: false, unlocked: false, pid: 0 };
      return ok("");
    }

    if (matches(args, ["version"])) {
      // version --check --json [--app-version X] [--refresh] [base]
      if (args.includes("--check")) {
        // Match the real CLI: exit 1 when anything is behind/skewed, but still
        // emit the JSON document on stdout so the app can render it.
        const doc =
          scenario.versionCheck ??
          (() => {
            const appIdx = args.indexOf("--app-version");
            const appVer = appIdx >= 0 ? (args[appIdx + 1] ?? "0.1.0") : undefined;
            const components: Array<{
              name: string;
              current: string;
              latest?: string;
              status: string;
              guide?: string;
            }> = [{ name: "cli", current: "v0.1.0-e2e", status: "dev" }];
            if (appVer) {
              components.push({
                name: "app",
                current: appVer.startsWith("v") ? appVer : `v${appVer}`,
                status: "dev",
              });
            }
            return { components };
          })();
        const body =
          typeof doc === "string" ? doc : JSON.stringify(doc, null, 2) + "\n";
        const parsed =
          typeof doc === "object" && doc !== null
            ? (doc as {
                components?: Array<{ status?: string }>;
                skew?: unknown;
              })
            : {};
        const behind =
          !!parsed.skew ||
          (parsed.components ?? []).some((c) => c.status === "behind");
        return {
          code: behind ? 1 : 0,
          stdout: body,
          stderr: behind
            ? "error: one or more OwnBase components are behind — see above\n"
            : "",
        };
      }
      return ok({
        version: "0.1.0-e2e",
        commit: "deadbeef",
        date: "2026-08-15",
        string: "ownbasectl 0.1.0-e2e (deadbeef 2026-08-15)",
      });
    }

    // Exact subcommand "list" (not "sessions list", etc.).
    if (matches(args, ["list"]) && stripMeta(args).length === 1) {
      if (!state.vault.unlocked) return err(7, "Error: the vault is locked");
      return ok(state.bases);
    }

    if (matches(args, ["keygen"])) {
      if (!state.vault.unlocked) return err(7, "Error: the vault is locked");
      const clean = stripMeta(args);
      const name = clean[clean.indexOf("keygen") + 1] ?? "base";
      const result = scenario.keygen ?? {
        base: name,
        public_key:
          "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE2EfixturePublicKey ownbase-e2e",
        created: true,
        stored_in: vaultPath,
      };
      return ok({ ...result, base: name });
    }

    if (matches(args, ["checkup"])) {
      if (!state.vault.unlocked) return err(7, "Error: the vault is locked");
      const clean = stripMeta(args);
      const name = clean[clean.indexOf("checkup") + 1] ?? "";
      const queue = checkupQueues[name];
      if (queue && queue.length > 0) return ok(queue.shift());
      const doc = scenario.checkup?.[name];
      if (doc) return ok(doc);
      // Fresh create/adopt paths land on the dashboard without a fixture —
      // return a minimal healthy document so the shell is usable.
      return ok({
        findings: [],
        status: {
          generated_at: "2026-08-15T12:00:00Z",
          schema_version: "1",
          version: "v0.4.0",
          services: [],
          jobs: [],
          security: {
            backup_restorable: true,
            drift_detected: false,
            exposure: {
              available: true,
              firewall_active: true,
              unexpected_count: 0,
            },
            access: {
              available: true,
              fail2ban_available: true,
              fail2ban_active: true,
              failed_attempts: 0,
            },
            vulns: {
              available: true,
              trivy_installed: true,
              host: { critical: 0, high: 0, medium: 0, low: 0 },
            },
          },
          updates: { drift: [] },
          audit: { total_seen: 0, recent_actions: [] },
        },
      });
    }

    if (matches(args, ["sessions", "list"])) {
      if (!state.vault.unlocked) return err(7, "Error: the vault is locked");
      return ok(scenario.sessions ?? []);
    }

    if (matches(args, ["sessions", "show"])) {
      if (!state.vault.unlocked) return err(7, "Error: the vault is locked");
      const clean = stripMeta(args);
      const id = clean[clean.indexOf("show") + 1] ?? "";
      if (args.includes("--cast")) {
        return ok(scenario.sessionCast?.[id] ?? "");
      }
      return ok(scenario.sessionTranscript?.[id] ?? "");
    }

    if (matches(args, ["service", "add"]) && args.includes("--dry-run")) {
      return ok(
        scenario.servicePreview ?? {
          status: "ok",
          would_change: true,
          commit_message: "service: add",
          diff: "+ service\n",
        },
      );
    }

    if (matches(args, ["service", "add"])) {
      return ok({
        status: "ok",
        would_change: true,
        commit_message: "service: add",
        diff: "",
      });
    }

    if (matches(args, ["service", "update"]) && args.includes("--dry-run")) {
      return ok(
        scenario.servicePreview ?? {
          status: "ok",
          would_change: true,
          commit_message: "service: update",
          diff: "~ service\n",
        },
      );
    }

    if (matches(args, ["service", "update"])) {
      return ok({
        status: "ok",
        would_change: true,
        commit_message: "service: update",
        diff: "",
      });
    }

    if (matches(args, ["service", "remove"]) && args.includes("--dry-run")) {
      return ok(
        scenario.servicePreview ?? {
          status: "ok",
          would_change: true,
          commit_message: "service: remove",
          diff: "- service\n",
        },
      );
    }

    if (matches(args, ["service", "remove"])) {
      return ok({
        status: "ok",
        would_change: true,
        commit_message: "service: remove",
        diff: "",
      });
    }

    if (matches(args, ["secrets", "list"])) {
      const clean = stripMeta(args);
      const after = clean.slice(clean.indexOf("list") + 1);
      if (after.length >= 2) {
        return ok({ service: after[1], keys: [...state.secretKeys] });
      }
      return ok({ services: ["web"] });
    }

    if (matches(args, ["secrets", "get"])) {
      const clean = stripMeta(args);
      const key = clean[clean.length - 1] ?? "KEY";
      // Per-key values so a wrong-key reveal fails the assertion.
      return ok({
        service: clean[clean.length - 2] ?? "web",
        key,
        value: `secret-value-for-${key}`,
      });
    }

    if (matches(args, ["secrets", "set"])) {
      return ok({
        status: "ok",
        service: stripMeta(args)[3] ?? "web",
        updated: 1,
      });
    }

    if (matches(args, ["secrets", "delete"])) {
      const clean = stripMeta(args);
      const key = clean[clean.length - 1] ?? "";
      state.secretKeys = state.secretKeys.filter((k) => k !== key);
      return ok({
        status: "ok",
        service: clean[3] ?? "web",
        deleted: key,
      });
    }

    if (matches(args, ["delete"])) {
      const clean = stripMeta(args);
      const name = clean[clean.indexOf("delete") + 1];
      state.bases = state.bases.filter((b) => b.name !== name);
      state.vault = { ...state.vault, bases: state.bases.length };
      return ok("");
    }

    if (matches(args, ["config", "get"])) {
      // cli.json appends --json; cli.text (configGetYAML) does not.
      if (args.includes("--json")) {
        return ok({ services: { web: { ref: "main" } } });
      }
      return ok(scenario.configYaml ?? "services:\n  web:\n    ref: main\n");
    }

    if (matches(args, ["config", "setup"])) {
      return ok({
        status: "ok",
        repo_url: args[args.indexOf("--repo") + 1] ?? "",
        ref: args.includes("--ref") ? args[args.indexOf("--ref") + 1] : "main",
        seeded: args.includes("--init"),
      });
    }

    if (matches(args, ["config", "set"]) && args.includes("--dry-run")) {
      return ok(
        scenario.servicePreview ?? {
          status: "ok",
          would_change: true,
          commit_message: "config: set",
          diff: "+ change\n",
        },
      );
    }

    if (matches(args, ["config", "set"])) {
      return ok({ status: "ok" });
    }

    if (matches(args, ["deploy"]) && args.includes("--dry-run")) {
      return ok(
        scenario.deployPreview ?? {
          status: "ok",
          would_change: true,
          commit_message: "deploy",
          diff: "~ ref\n",
        },
      );
    }

    if (matches(args, ["deploy"])) {
      return ok({
        status: "ok",
        service: stripMeta(args)[2] ?? "web",
        ref: args.includes("--ref") ? args[args.indexOf("--ref") + 1] : "main",
      });
    }

    if (matches(args, ["backup", "setup"]) && args.includes("--dry-run")) {
      return ok(
        scenario.backupSetupPreview ?? {
          status: "ok",
          would_change: true,
          commit_message: "backup: setup",
          diff: "+ backup\n",
        },
      );
    }

    if (matches(args, ["backup", "setup"])) {
      return ok("backup configured\nfirst snapshot done\n");
    }

    if (matches(args, ["backup", "run"])) {
      return ok("snapshot saved\n");
    }

    if (matches(args, ["backup", "prune"])) {
      return ok("pruned 2 snapshots\n");
    }

    if (matches(args, ["backup", "rekey"])) {
      const stderr =
        scenario.rekeyStderr ??
        "Generated restic password (save this):\n  e2e-generated-restic-pw\nrekey done\n";
      // backupRekey uses cli.raw and reads stderr even on success.
      return {
        code: 0,
        stdout: JSON.stringify({ status: "ok" }) + "\n",
        stderr,
      };
    }

    if (matches(args, ["backup", "recovery-kit"])) {
      return ok(
        scenario.recoveryKit ?? {
          repo: "s3:bucket/path",
          password: "kit-password",
          note: "store offline",
        },
      );
    }

    if (matches(args, ["db", "status"])) {
      return ok(
        scenario.dbStatus ?? {
          stanza: "main",
          stanza_ok: true,
          earliest_recovery: "2026-08-01T00:00:00Z",
          latest_recovery: "2026-08-15T12:00:00Z",
        },
      );
    }

    if (matches(args, ["db", "restore"])) {
      return ok(
        scenario.dbRestore ?? {
          into: args.includes("--into")
            ? args[args.indexOf("--into") + 1]
            : "scratch",
          scratch_endpoint: "localhost:5433",
          databases: 1,
        },
      );
    }

    if (matches(args, ["ssh-key", "list"]) || matches(args, ["ssh-key", "add"])) {
      return ok({
        public_key:
          "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE2EdeployKey ownbase-deploy",
      });
    }

    if (matches(args, ["upgrade"]) && !args.includes("--apply")) {
      return ok(
        scenario.upgradeCheck ?? {
          status: "ok",
          packages: [
            {
              name: "caddy",
              image: "ghcr.io/ownbase/caddy:2",
              digest: "sha256:abc",
              running: true,
            },
          ],
        },
      );
    }

    if (matches(args, ["security", "scan"])) {
      return ok("scan accepted\n");
    }

    // Streamed ops that somehow arrived as cli_run (shouldn't, but don't hang).
    if (
      matches(args, ["security", "fix"]) ||
      matches(args, ["security", "reboot"]) ||
      matches(args, ["security", "install-scanner"]) ||
      matches(args, ["self-update"]) ||
      (matches(args, ["upgrade"]) && args.includes("--apply"))
    ) {
      return ok("ok\n");
    }

    return err(
      1,
      `Error: e2e mock has no handler for: ownbasectl ${args.join(" ")}\n` +
        "Add a fail/stream rule or extend e2e/shim/install.ts.",
    );
  }

  async function handleStream(
    id: string,
    args: string[],
    stdin: string | null,
    onEvent: unknown,
  ): Promise<void> {
    const ch = channelId(onEvent);
    if (ch == null) throw new Error("cli_stream missing channel");

    state.streams.set(id, { cancelled: false });

    const fail = findFail(args);
    let events: SE[];
    if (fail) {
      events = [
        { kind: "stderr", line: fail.stderr.replace(/\n$/, "") },
        { kind: "finished", code: fail.code },
      ];
    } else {
      const rule = findStream(args);
      if (rule) {
        events = rule.events as SE[];
      } else if (matches(args, ["create"])) {
        const clean = stripMeta(args);
        const name = clean[clean.indexOf("create") + 1] ?? "new";
        events = [
          { kind: "stderr", line: `creating ${name}…` },
          { kind: "stderr", line: "Base ready." },
          { kind: "finished", code: 0 },
        ];
      } else if (matches(args, ["adopt"]) || matches(args, ["restore"])) {
        events = [
          { kind: "stderr", line: "connecting…" },
          { kind: "stderr", line: "done." },
          { kind: "finished", code: 0 },
        ];
      } else if (
        matches(args, ["security"]) ||
        matches(args, ["self-update"]) ||
        matches(args, ["upgrade"]) ||
        matches(args, ["checkup"]) ||
        matches(args, ["backup"])
      ) {
        // Generic success stream for host actions and verify drills.
        events = [
          { kind: "stderr", line: "working…" },
          { kind: "stderr", line: "done." },
          { kind: "finished", code: 0 },
        ];
      } else {
        events = [
          {
            kind: "stderr",
            line: `Error: e2e mock has no stream handler for: ${args.join(" ")}`,
          },
          { kind: "finished", code: 1 },
        ];
      }
    }

    // Successful create registers the Base so the shell can open it afterwards
    // (custom stream fixtures and the default path both need this).
    const finishedOk = events.some((e) => e.kind === "finished" && e.code === 0);
    if (finishedOk && matches(args, ["create"])) {
      const clean = stripMeta(args);
      const name = clean[clean.indexOf("create") + 1] ?? "new";
      if (!state.bases.find((b) => b.name === name)) {
        state.bases.push({
          name,
          host: args.includes("--remote") ? "203.0.113.10" : "192.168.64.99",
          kind: args.includes("--remote") ? "remote" : "vm",
          vm_state: "Running",
          registered: true,
          ssh_user: "ubuntu",
          ssh_port: 22,
          has_token: true,
          has_key: true,
        });
        state.vault = {
          ...state.vault,
          bases: state.bases.length,
          keys: state.bases.length,
        };
      }
    }

    const delay = findStream(args)?.delayMs ?? 0;
    let index = 0;
    for (const event of events) {
      if (state.streams.get(id)?.cancelled) {
        runCallback(ch, { index, message: { kind: "finished", code: 130 } });
        index++;
        break;
      }
      if (delay > 0) await new Promise((r) => setTimeout(r, delay));
      runCallback(ch, { index, message: event });
      index++;
      if (event.kind === "finished" || event.kind === "failed") break;
    }
    runCallback(ch, { index, end: true });
    void stdin;
  }

  async function invoke(cmd: string, args: Record<string, unknown> = {}): Promise<unknown> {
    if (phase === "broken") {
      throw new Error("e2e: CLI sidecar is broken");
    }

    if (cmd === "cli_run") {
      const argv = (args.args as string[]) ?? [];
      const stdin = (args.stdin as string | null) ?? null;
      state.calls.push({ cmd: "cli_run", args: argv, stdin });
      return handleRun(argv, stdin);
    }

    if (cmd === "cli_stream") {
      const id = String(args.id ?? "");
      const argv = (args.args as string[]) ?? [];
      const stdin = (args.stdin as string | null) ?? null;
      state.calls.push({ cmd: "cli_stream", args: argv, stdin, id });
      void handleStream(id, argv, stdin, args.onEvent).catch((err: unknown) => {
        const message = err instanceof Error ? err.message : String(err);
        console.error("[e2e shim] cli_stream failed:", message);
      });
      return null;
    }

    if (cmd === "cli_cancel") {
      const id = String(args.id ?? "");
      state.calls.push({ cmd: "cli_cancel", id });
      const s = state.streams.get(id);
      if (s) s.cancelled = true;
      return null;
    }

    if (cmd === "plugin:dialog|open") {
      state.calls.push({ cmd: "plugin:dialog|open" });
      const fallback = vaultPath.includes("/")
        ? vaultPath.slice(0, vaultPath.lastIndexOf("/"))
        : "/tmp/ownbase-e2e";
      return scenario.dialogPath ?? fallback;
    }

    if (cmd === "plugin:dialog|save" || cmd === "plugin:dialog|message") {
      return null;
    }

    throw new Error(`e2e mock: unhandled invoke command ${cmd}`);
  }

  (window as unknown as { __TAURI_INTERNALS__: unknown }).__TAURI_INTERNALS__ = {
    invoke,
    transformCallback,
    unregisterCallback,
    runCallback,
    callbacks,
    convertFileSrc: (path: string) => path,
    metadata: {
      currentWindow: { label: "main" },
      currentWebview: { windowLabel: "main", label: "main" },
    },
  };
  (window as unknown as { __TAURI_EVENT_PLUGIN_INTERNALS__: unknown }).__TAURI_EVENT_PLUGIN_INTERNALS__ =
    {
      unregisterListener: () => {},
    };
}

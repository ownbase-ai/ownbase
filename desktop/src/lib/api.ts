// Named operations, so the rest of the app never assembles an argv.
//
// Keeping the argument lists in one file means a CLI flag rename is one edit,
// and it makes the app's whole surface area readable at a glance: this is
// everything the window can cause to happen.

import * as cli from "./cli";
import type {
  BaseSummary,
  Checkup,
  CliVersion,
  ConfigPreview,
  DBRestoreOutcome,
  DBStatus,
  KeygenResult,
  RecoveryKit,
  SecretValue,
  SecretsDeleteResult,
  SecretsKeysList,
  SecretsServicesList,
  SecretsSetResult,
  SessionMeta,
  UpgradeCheck,
  VaultInitResult,
  VaultRecoveryString,
  VaultStatus,
  VersionCheck,
} from "./types";

// ---------------------------------------------------------------------------
// Vault and agent
// ---------------------------------------------------------------------------

/**
 * The vault's state.
 *
 * `vault status` deliberately does not start the agent — "is it running?" has
 * to be answerable without changing the answer — so this is safe to poll.
 */
export function vaultStatus(): Promise<VaultStatus> {
  return cli.json<VaultStatus>(["vault", "status"]);
}

/**
 * Create a vault, or adopt one that already exists at this path.
 *
 * The password goes over stdin, never in argv: argv is readable by any process
 * on the machine through `ps` for as long as the command runs.
 */
export function vaultInit(path: string, password: string): Promise<VaultInitResult> {
  return cli.json<VaultInitResult>(
    ["vault", "init", path, "--password-stdin"],
    password,
  );
}

export function vaultUnlock(
  password: string,
  idleTimeout?: string,
): Promise<VaultStatus> {
  const args = ["vault", "unlock", "--password-stdin"];
  if (idleTimeout) args.push("--idle-timeout", idleTimeout);
  return cli.json<VaultStatus>(args, password);
}

export async function vaultLock(): Promise<void> {
  await cli.text(["vault", "lock"]);
}

export async function vaultChangePassword(newPassword: string): Promise<void> {
  await cli.text(["vault", "passwd", "--password-stdin"], newPassword);
}

export async function agentStop(): Promise<void> {
  await cli.text(["agent", "stop"]);
}

/** `ownbasectl vault recovery-string --json` — remote vaults only. */
export function vaultRecoveryString(): Promise<VaultRecoveryString> {
  return cli.json<VaultRecoveryString>(["vault", "recovery-string"]);
}

/**
 * Point this machine at a vault from its recovery string and unlock it.
 * Password over stdin; recovery string is a flag (matches the CLI).
 */
export function vaultOpen(
  recovery: string,
  password: string,
): Promise<VaultStatus> {
  return cli.json<VaultStatus>(
    ["vault", "open", "--recovery", recovery, "--password-stdin"],
    password,
  );
}

/** Bundled ownbasectl version. */
export function cliVersion(): Promise<CliVersion> {
  return cli.json<CliVersion>(["version"]);
}

/**
 * Compare running components against the newest release.
 *
 * Pass `appVersion` (from `getVersion()`) so the app is included. Pass `base`
 * to also check that Base's daemon and CLI/daemon skew.
 */
export function versionCheck(opts: {
  appVersion?: string;
  base?: string;
  refresh?: boolean;
} = {}): Promise<VersionCheck> {
  const args = ["version", "--check", "--json"];
  if (opts.refresh) args.push("--refresh");
  if (opts.appVersion) args.push("--app-version", opts.appVersion);
  if (opts.base) args.push(opts.base);
  return cli.json<VersionCheck>(args);
}

/** Desktop app version stamped into the bundle at release (Tauri). */
export async function appVersion(): Promise<string> {
  const { getVersion } = await import("@tauri-apps/api/app");
  return getVersion();
}

// ---------------------------------------------------------------------------
// Bases
// ---------------------------------------------------------------------------

export function listBases(): Promise<BaseSummary[]> {
  return cli.json<BaseSummary[]>(["list"]).then((bases) => bases ?? []);
}

/**
 * Forget a Base on this computer: drop its vault profile and owner key.
 *
 * Always passes `--yes` — the window confirms first. `keepVm` leaves a local
 * Multipass VM running (and is the only safe choice for a remote server; the
 * CLI never destroys one of those either way). Without it, a local VM is
 * destroyed and its data is gone.
 */
export async function deleteBase(
  base: string,
  opts: { keepVm?: boolean } = {},
): Promise<void> {
  const args = ["delete", base, "--yes"];
  if (opts.keepVm) args.push("--keep-vm");
  await cli.text(args);
}

/** Create or reuse the owner key for a Base, returning the public half. */
export function keygen(base: string): Promise<KeygenResult> {
  return cli.json<KeygenResult>(["keygen", base]);
}

/**
 * Import a private key file you already have as a Base's owner key, instead
 * of generating one. The file is read once and copied into the vault; it is
 * left alone on disk.
 */
export function keygenImport(base: string, path: string): Promise<KeygenResult> {
  return cli.json<KeygenResult>(["keygen", base, "--import", path]);
}

/**
 * Provision a Base, streaming progress.
 *
 * With `remote`, installs on a fresh Ubuntu server. Without it, launches a
 * local Multipass VM. `--wait` is not optional here: the whole point of
 * showing this in a window is that the user watches hardening finish rather
 * than being told "done" while the daemon is still working.
 */
export function createBase(
  base: string,
  opts: {
    remote?: string;
    caddyEmail?: string;
    sshUser?: string;
    sshPort?: number;
    replace?: boolean;
    cpus?: number;
    memory?: number;
    disk?: number;
  },
  onEvent: (event: cli.StreamEvent) => void,
): cli.StreamHandle {
  const args = ["create", base, "--wait", "--yes"];
  if (opts.remote) args.push("--remote", opts.remote);
  if (opts.caddyEmail) args.push("--caddy-email", opts.caddyEmail);
  if (opts.sshUser) args.push("--ssh-user", opts.sshUser);
  if (opts.sshPort) args.push("--ssh-port", String(opts.sshPort));
  if (opts.replace) args.push("--replace");
  if (opts.cpus) args.push("--cpus", String(opts.cpus));
  if (opts.memory) args.push("--memory", String(opts.memory));
  if (opts.disk) args.push("--disk", String(opts.disk));
  return cli.stream(args, onEvent);
}

/**
 * Reconstruct a Base from backups onto a fresh VM or server. Streams progress.
 * Credentials go over stdin via --creds-stdin (never in argv).
 */
export function restoreBase(
  base: string,
  opts: {
    repo?: string;
    password?: string;
    remote?: string;
    sshUser?: string;
    sshPort?: number;
    caddyEmail?: string;
    cpus?: number;
    memory?: number;
    disk?: number;
    forceRebuild?: boolean;
    aws_access_key_id?: string;
    aws_secret_access_key?: string;
    b2_account_id?: string;
    b2_account_key?: string;
  },
  onEvent: (event: cli.StreamEvent) => void,
): cli.StreamHandle {
  const args = ["restore", base, "--wait", "--yes", "--creds-stdin"];
  if (opts.repo) args.push("--repo", opts.repo);
  if (opts.remote) args.push("--remote", opts.remote);
  if (opts.sshUser) args.push("--ssh-user", opts.sshUser);
  if (opts.sshPort) args.push("--ssh-port", String(opts.sshPort));
  if (opts.caddyEmail) args.push("--caddy-email", opts.caddyEmail);
  if (opts.cpus) args.push("--cpus", String(opts.cpus));
  if (opts.memory) args.push("--memory", String(opts.memory));
  if (opts.disk) args.push("--disk", String(opts.disk));
  if (opts.forceRebuild) args.push("--force");
  const creds = JSON.stringify({
    password: opts.password || "",
    aws_access_key_id: opts.aws_access_key_id || "",
    aws_secret_access_key: opts.aws_secret_access_key || "",
    b2_account_id: opts.b2_account_id || "",
    b2_account_key: opts.b2_account_key || "",
  });
  return cli.stream(args, onEvent, creds);
}

/**
 * Register a Base that already has OwnBase running on it — someone else
 * provisioned it, or it's already known to another copy of this vault.
 *
 * Streamed rather than a single call because `adopt` prints its own progress
 * ("verifying SSH connection...", "connected to..."), and that is worth
 * showing even though the whole thing takes seconds, not the minutes `create`
 * does. `sshKeyPath` is the private key file already authorized on that
 * server; nothing is written to the vault unless the connection it proves
 * actually works.
 */
export function adoptBase(
  base: string,
  opts: { host: string; sshUser: string; sshPort: number; sshKeyPath: string; apiPort?: number },
  onEvent: (event: cli.StreamEvent) => void,
): cli.StreamHandle {
  const args = [
    "adopt",
    base,
    "--host",
    opts.host,
    "--ssh-user",
    opts.sshUser,
    "--ssh-port",
    String(opts.sshPort),
    "--ssh-key",
    opts.sshKeyPath,
  ];
  if (opts.apiPort) args.push("--api-port", String(opts.apiPort));
  return cli.stream(args, onEvent);
}

/**
 * Everything the app knows about one Base: its own status document plus the
 * CLI's verdict on it.
 *
 * One call, not five. `security`, `updates`, and `backup status` are each a
 * section of this same document, and every one of them costs a fresh SSH
 * tunnel — so the detail view fetches once and renders the sections from it.
 *
 * The findings come from `checkup` rather than being recomputed here on
 * purpose. Deciding what counts as a problem is the CLI's job; a second
 * opinion in TypeScript would drift, and then the app and the terminal would
 * disagree about whether a Base is healthy.
 */
export function checkup(base: string): Promise<Checkup> {
  return cli.json<Checkup>(["checkup", base]);
}

/**
 * Run the verified-restore drill and report what it found.
 *
 * Minutes of real work — the Base restores its newest snapshot and starts a
 * database from it — so progress streams instead of the window hanging.
 */
export function verifyBackup(
  base: string,
  onEvent: (event: cli.StreamEvent) => void,
): cli.StreamHandle {
  return cli.stream(["checkup", base, "--verify", "--json"], onEvent);
}

/** Trigger a snapshot now. */
export async function backupNow(base: string): Promise<string> {
  return cli.text(["backup", "run", base]);
}

// ---------------------------------------------------------------------------
// Security actions (host packages; do not change desired state)
// ---------------------------------------------------------------------------

/**
 * Apply available host OS package patches on the Base.
 * With reboot, chains into a wait-for-return if a reboot is required.
 * Streams apt (and reboot) output.
 */
export function securityFix(
  base: string,
  onEvent: (event: cli.StreamEvent) => void,
  opts?: { reboot?: boolean },
): cli.StreamHandle {
  const args = ["security", "fix", base];
  if (opts?.reboot) args.push("--reboot");
  return cli.stream(args, onEvent);
}

/**
 * Trigger an immediate CVE rescan. Returns once the daemon has accepted the
 * job; results land in status a few minutes later. With wait, blocks until
 * the scan finishes.
 */
export async function securityScan(
  base: string,
  opts?: { wait?: boolean },
): Promise<string> {
  const args = ["security", "scan", base];
  if (opts?.wait) args.push("--wait");
  return cli.text(args);
}

/**
 * Schedule a reboot on the Base so applied package upgrades (typically a new
 * kernel) take effect. With wait, blocks until the API answers again.
 */
export function securityReboot(
  base: string,
  onEvent: (event: cli.StreamEvent) => void,
  opts?: { wait?: boolean },
): cli.StreamHandle {
  const args = ["security", "reboot", base];
  if (opts?.wait) args.push("--wait");
  return cli.stream(args, onEvent);
}

/** Install trivy + enable podman.socket on the Base. Streams progress. */
export function installScanner(
  base: string,
  onEvent: (event: cli.StreamEvent) => void,
): cli.StreamHandle {
  return cli.stream(["security", "install-scanner", base], onEvent);
}

/** Replace the OwnBase daemon with a newer signed release. Streams progress. */
export function selfUpdate(
  base: string,
  onEvent: (event: cli.StreamEvent) => void,
  version = "latest",
): cli.StreamHandle {
  return cli.stream(["self-update", base, "--version", version], onEvent);
}

/** Pull the pinned core package image (Caddy) and restart it. */
export function upgradeApply(
  base: string,
  onEvent: (event: cli.StreamEvent) => void,
): cli.StreamHandle {
  return cli.stream(["upgrade", base, "--apply"], onEvent);
}

/** Core package status (Caddy) without applying. */
export function upgradeCheck(base: string): Promise<UpgradeCheck> {
  return cli.json<UpgradeCheck>(["upgrade", base]);
}

/** Dry-run a deploy; returns the would-be ownbase.yaml diff. */
export function deployPreview(
  base: string,
  service: string,
  ref: string,
): Promise<ConfigPreview> {
  return cli.json<ConfigPreview>([
    "deploy",
    base,
    service,
    "--ref",
    ref,
    "--dry-run",
  ]);
}

/** Commit + push a deploy and trigger reconcile. */
export async function deploy(
  base: string,
  service: string,
  ref: string,
): Promise<{ status: string; service: string; ref: string }> {
  return cli.json(["deploy", base, service, "--ref", ref]);
}

// ---------------------------------------------------------------------------
// Secrets (values over stdin for set; reveal is explicit per-key)
// ---------------------------------------------------------------------------

export function secretsListServices(base: string): Promise<SecretsServicesList> {
  return cli.json<SecretsServicesList>(["secrets", "list", base]);
}

export function secretsListKeys(
  base: string,
  service: string,
): Promise<SecretsKeysList> {
  return cli.json<SecretsKeysList>(["secrets", "list", base, service]);
}

export function secretsGet(
  base: string,
  service: string,
  key: string,
): Promise<SecretValue> {
  return cli.json<SecretValue>(["secrets", "get", base, service, key]);
}

/** Set secrets; values travel as JSON on stdin (never argv). */
export function secretsSet(
  base: string,
  service: string,
  values: Record<string, string>,
): Promise<SecretsSetResult> {
  return cli.json<SecretsSetResult>(
    ["secrets", "set", base, service, "--stdin"],
    JSON.stringify(values),
  );
}

export function secretsDelete(
  base: string,
  service: string,
  key: string,
): Promise<SecretsDeleteResult> {
  return cli.json<SecretsDeleteResult>(["secrets", "delete", base, service, key]);
}

// ---------------------------------------------------------------------------
// Service editing (config-repo commits; always dry-run then confirm)
// ---------------------------------------------------------------------------

export interface ServiceFields {
  repo?: string;
  ref?: string;
  dockerfile?: string;
  context?: string;
  port?: number;
  domain?: string;
  domains?: string[];
  internal?: boolean;
  dataPath?: string;
  database?: string;
  requires?: string[];
  env?: string[];
  addCapabilities?: string[];
  ownbaseAccess?: string[];
  replicas?: number;
}

function pushServiceFields(args: string[], fields: ServiceFields): void {
  if (fields.repo) args.push("--repo", fields.repo);
  if (fields.ref) args.push("--ref", fields.ref);
  if (fields.dockerfile) args.push("--dockerfile", fields.dockerfile);
  if (fields.context) args.push("--context", fields.context);
  if (fields.port !== undefined) args.push("--port", String(fields.port));
  if (fields.domain) args.push("--domain", fields.domain);
  if (fields.domains) {
    for (const d of fields.domains) args.push("--domains", d);
  }
  if (fields.internal) args.push("--internal");
  if (fields.dataPath) args.push("--data-path", fields.dataPath);
  if (fields.database) args.push("--database", fields.database);
  if (fields.requires) {
    for (const r of fields.requires) args.push("--requires", r);
  }
  if (fields.env) {
    for (const e of fields.env) args.push("--env", e);
  }
  if (fields.addCapabilities) {
    for (const c of fields.addCapabilities) args.push("--add-capabilities", c);
  }
  if (fields.ownbaseAccess) {
    for (const s of fields.ownbaseAccess) args.push("--ownbase-access", s);
  }
  if (fields.replicas !== undefined) args.push("--replicas", String(fields.replicas));
}

export function serviceAddPreview(
  base: string,
  service: string,
  fields: ServiceFields,
): Promise<ConfigPreview> {
  const args = ["service", "add", base, service, "--dry-run"];
  pushServiceFields(args, fields);
  return cli.json<ConfigPreview>(args);
}

export function serviceAdd(
  base: string,
  service: string,
  fields: ServiceFields,
): Promise<{ status: string; service: string }> {
  const args = ["service", "add", base, service];
  pushServiceFields(args, fields);
  return cli.json(args);
}

export function serviceUpdatePreview(
  base: string,
  service: string,
  fields: ServiceFields,
): Promise<ConfigPreview> {
  const args = ["service", "update", base, service, "--dry-run"];
  pushServiceFields(args, fields);
  return cli.json<ConfigPreview>(args);
}

export function serviceUpdate(
  base: string,
  service: string,
  fields: ServiceFields,
): Promise<{ status: string; service: string }> {
  const args = ["service", "update", base, service];
  pushServiceFields(args, fields);
  return cli.json(args);
}

export function serviceRemovePreview(
  base: string,
  service: string,
): Promise<ConfigPreview> {
  return cli.json<ConfigPreview>(["service", "remove", base, service, "--dry-run"]);
}

export function serviceRemove(
  base: string,
  service: string,
): Promise<{ status: string; service: string }> {
  return cli.json(["service", "remove", base, service]);
}

/** Current ownbase.yaml as a JSON document (decoded YAML). */
export function configGet(base: string): Promise<unknown> {
  return cli.json(["config", "get", base]);
}

/** Raw ownbase.yaml text from the Base (no --json). */
export function configGetYAML(base: string): Promise<string> {
  return cli.text(["config", "get", base]);
}

export function configSetPreview(
  base: string,
  yaml: string,
  message?: string,
): Promise<ConfigPreview> {
  const args = ["config", "set", base, "--file", "-", "--dry-run"];
  if (message) args.push("--message", message);
  return cli.json<ConfigPreview>(args, yaml);
}

export function configSet(
  base: string,
  yaml: string,
  message?: string,
): Promise<{ status: string }> {
  const args = ["config", "set", base, "--file", "-"];
  if (message) args.push("--message", message);
  return cli.json(args, yaml);
}

export interface BackupSetupInput {
  repo: string;
  password: string;
  aws_access_key_id?: string;
  aws_secret_access_key?: string;
  b2_account_id?: string;
  b2_account_key?: string;
  interval?: string;
  verify_interval?: string;
}

/** Dry-run backup setup; returns the would-be ownbase.yaml diff. No secrets written. */
export function backupSetupPreview(
  base: string,
  input: BackupSetupInput,
): Promise<ConfigPreview> {
  const args = ["backup", "setup", base, "--repo", input.repo, "--dry-run"];
  if (input.interval) args.push("--interval", input.interval);
  if (input.verify_interval) args.push("--verify-interval", input.verify_interval);
  return cli.json<ConfigPreview>(args);
}

/**
 * Run backup setup with credentials over stdin (never in argv).
 * Progress is returned as the full stdout once complete — the first snapshot
 * can take minutes.
 */
export async function backupSetupRun(
  base: string,
  input: BackupSetupInput,
): Promise<string> {
  const args = ["backup", "setup", base, "--repo", input.repo, "--creds-stdin"];
  if (input.interval) args.push("--interval", input.interval);
  if (input.verify_interval) args.push("--verify-interval", input.verify_interval);
  const creds = JSON.stringify({
    password: input.password,
    aws_access_key_id: input.aws_access_key_id || "",
    aws_secret_access_key: input.aws_secret_access_key || "",
    b2_account_id: input.b2_account_id || "",
    b2_account_key: input.b2_account_key || "",
  });
  return cli.text(args, creds);
}

/** Ensure the Base has a git deploy key; returns the public half to register. */
export function sshKeyAdd(
  base: string,
  host = "github.com",
): Promise<{ public_key: string }> {
  return cli.json(["ssh-key", "add", base, "--host", host]);
}

/** Current git deploy public key, if any. */
export function sshKeyList(base: string): Promise<{ public_key: string }> {
  return cli.json(["ssh-key", "list", base]);
}

/** Point the Base at its external config repo (optionally seed ownbase.yaml). */
export function configSetup(
  base: string,
  opts: { repo: string; ref?: string; init?: boolean },
): Promise<{ status: string; repo_url: string; ref: string; seeded: boolean }> {
  // cli.json appends --json; do not add it here.
  const args = ["config", "setup", base, "--repo", opts.repo];
  if (opts.ref) args.push("--ref", opts.ref);
  if (opts.init) args.push("--init");
  return cli.json(args);
}

// ---------------------------------------------------------------------------
// Backup lifecycle
// ---------------------------------------------------------------------------

export async function backupPrune(base: string): Promise<string> {
  return cli.text(["backup", "prune", base]);
}

/**
 * Rotate the restic password. Prefer generate=true. With generate, the new
 * password is printed on stderr immediately so an interrupted rekey is
 * recoverable — always surface `generated_password` to the user.
 */
export async function backupRekey(
  base: string,
  opts: { generate?: boolean; newPassword?: string },
): Promise<{
  result: Record<string, unknown>;
  /** Present when --generate was used; take from stderr even on failure. */
  generated_password?: string;
  stderr: string;
}> {
  const args = ["backup", "rekey", base, "--json"];
  if (opts.generate) args.push("--generate");
  else if (opts.newPassword) {
    // Prefer generate. Manual password still has to be a flag today (no stdin).
    args.push("--new-password", opts.newPassword);
  }
  const raw = await cli.raw(args);
  const generated = parseGeneratedResticPassword(raw.stderr);
  if (raw.code !== cli.Exit.ok) {
    const err = new cli.CliError(args, raw);
    if (generated) {
      (err as Error & { generated_password?: string }).generated_password = generated;
    }
    throw err;
  }
  let result: Record<string, unknown> = {};
  try {
    result = JSON.parse(raw.stdout) as Record<string, unknown>;
  } catch {
    /* human leftover */
  }
  return { result, generated_password: generated, stderr: raw.stderr };
}

function parseGeneratedResticPassword(stderr: string): string | undefined {
  // CLI prints: "Generated restic password ...:\n  <password>\n"
  const lines = stderr.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? "";
    if (/Generated restic password/i.test(line)) {
      const next = (lines[i + 1] ?? "").trim();
      if (next) return next;
    }
  }
  return undefined;
}

/** Restic recovery kit from vault escrow (secrets on purpose). */
export function backupRecoveryKit(base: string): Promise<RecoveryKit> {
  return cli.json<RecoveryKit>(["backup", "recovery-kit", base]);
}

// ---------------------------------------------------------------------------
// Postgres recovery
// ---------------------------------------------------------------------------

export function dbStatus(base: string): Promise<DBStatus> {
  return cli.json<DBStatus>(["db", "status", base]);
}

/**
 * Point-in-time Postgres restore. Defaults to scratch. Production requires the
 * window to confirm first; --yes is always passed because the app is the
 * confirmation layer (confirm() auto-approves on non-TTY).
 */
export async function dbRestore(
  base: string,
  opts: { to?: string; into?: "scratch" | "production"; scratchPort?: number } = {},
): Promise<DBRestoreOutcome> {
  const args = ["db", "restore", base, "--yes"];
  if (opts.to) args.push("--to", opts.to);
  if (opts.into) args.push("--into", opts.into);
  if (opts.scratchPort) args.push("--scratch-port", String(opts.scratchPort));
  return cli.json<DBRestoreOutcome>(args);
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

export function listSessions(base?: string): Promise<SessionMeta[]> {
  const args = ["sessions", "list"];
  if (base) args.push(base);
  return cli.json<SessionMeta[]>(args).then((sessions) => sessions ?? []);
}

/** The recording itself, asciicast v2, for the player. */
export function sessionCast(id: string): Promise<string> {
  return cli.text(["sessions", "show", id, "--cast"]);
}

/** The recording as plain text, for reading and for copying out. */
export function sessionTranscript(id: string): Promise<string> {
  return cli.text(["sessions", "show", id]);
}

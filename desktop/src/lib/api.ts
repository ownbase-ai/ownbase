// Named operations, so the rest of the app never assembles an argv.
//
// Keeping the argument lists in one file means a CLI flag rename is one edit,
// and it makes the app's whole surface area readable at a glance: this is
// everything the window can cause to happen.

import * as cli from "./cli";
import type {
  BaseSummary,
  Checkup,
  ConfigPreview,
  KeygenResult,
  SessionMeta,
  VaultInitResult,
  VaultStatus,
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
  opts: { remote?: string; caddyEmail?: string; sshUser?: string; sshPort?: number },
  onEvent: (event: cli.StreamEvent) => void,
): cli.StreamHandle {
  const args = ["create", base, "--wait", "--yes"];
  if (opts.remote) args.push("--remote", opts.remote);
  if (opts.caddyEmail) args.push("--caddy-email", opts.caddyEmail);
  if (opts.sshUser) args.push("--ssh-user", opts.sshUser);
  if (opts.sshPort) args.push("--ssh-port", String(opts.sshPort));
  return cli.stream(args, onEvent);
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
 * Apply available host OS package patches on the Base (`apt-get upgrade`).
 * Streams apt output. Triggers an automatic CVE rescan on completion.
 */
export function securityFix(
  base: string,
  onEvent: (event: cli.StreamEvent) => void,
): cli.StreamHandle {
  return cli.stream(["security", "fix", base], onEvent);
}

/**
 * Trigger an immediate CVE rescan. Returns once the daemon has accepted the
 * job; results land in status a few minutes later.
 */
export async function securityScan(base: string): Promise<string> {
  return cli.text(["security", "scan", base]);
}

/**
 * Schedule a reboot on the Base so applied package upgrades (typically a new
 * kernel) take effect. Streams a short confirmation; the machine goes down
 * about a minute later.
 */
export function securityReboot(
  base: string,
  onEvent: (event: cli.StreamEvent) => void,
): cli.StreamHandle {
  return cli.stream(["security", "reboot", base], onEvent);
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

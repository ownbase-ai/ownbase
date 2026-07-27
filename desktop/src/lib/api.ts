// Named operations, so the rest of the app never assembles an argv.
//
// Keeping the argument lists in one file means a CLI flag rename is one edit,
// and it makes the app's whole surface area readable at a glance: this is
// everything the window can cause to happen.

import * as cli from "./cli";
import type {
  BaseSummary,
  Checkup,
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

/** Create or reuse the owner key for a Base, returning the public half. */
export function keygen(base: string): Promise<KeygenResult> {
  return cli.json<KeygenResult>(["keygen", base]);
}

/**
 * Provision a Base, streaming progress.
 *
 * `--wait` is not optional here: the whole point of showing this in a window is
 * that the user watches hardening finish rather than being told "done" while
 * the daemon is still working.
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

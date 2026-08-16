// The single bridge between the window and everything else.
//
// Every screen in this app is a rendering of `ownbasectl --json`. Nothing here
// talks to a Base, opens SSH, or reads the vault; it runs the CLI and parses
// what came back. That is what keeps the app from becoming a second control
// plane that could disagree with the CLI about what is deployed.

import { invoke, Channel } from "@tauri-apps/api/core";

/** Exit codes `ownbasectl` uses, which the UI reacts to rather than just shows. */
export const Exit = {
  ok: 0,
  error: 1,
  usage: 2,
  preflight: 3,
  install: 4,
  notReady: 5,
  conflict: 6,
  /** The vault is locked, or there is no vault yet. */
  locked: 7,
} as const;

interface CliResult {
  code: number;
  stdout: string;
  stderr: string;
}

/**
 * A command that exited non-zero.
 *
 * `code` is the interesting part: the CLI classifies its failures, so the UI
 * can send the user to the unlock screen for 7 or explain a preflight failure
 * for 3 rather than showing every problem as the same red box.
 */
export class CliError extends Error {
  readonly code: number;
  readonly stdout: string;
  readonly stderr: string;
  readonly args: string[];

  constructor(args: string[], result: CliResult) {
    super(cliMessage(result) || `ownbasectl ${args.join(" ")} exited ${result.code}`);
    this.name = "CliError";
    this.code = result.code;
    this.stdout = result.stdout;
    this.stderr = result.stderr;
    this.args = args;
  }

  /** True when the vault needs unlocking before anything else can work. */
  get isLocked(): boolean {
    return this.code === Exit.locked;
  }
}

/**
 * The line worth showing a person.
 *
 * `ownbasectl` writes progress and errors to stderr and prefixes the failure
 * with "Error:". The last non-empty line is the specific complaint; earlier
 * lines are usually context that is already on screen.
 */
function cliMessage(result: CliResult): string {
  const lines = result.stderr
    .split("\n")
    .map((l) => l.replace(/^Error:\s*/, "").trim())
    .filter(Boolean);
  return lines.at(-1) ?? "";
}

/** Run `ownbasectl` and hand back the raw result, non-zero exits included. */
export async function raw(args: string[], stdin?: string): Promise<CliResult> {
  return invoke<CliResult>("cli_run", { args, stdin: stdin ?? null });
}

/** Run `ownbasectl`, throwing CliError on a non-zero exit. */
export async function text(args: string[], stdin?: string): Promise<string> {
  const result = await raw(args, stdin);
  if (result.code !== Exit.ok) throw new CliError(args, result);
  return result.stdout;
}

/**
 * Run `ownbasectl ... --json` and parse the document it printed.
 *
 * `--json` is appended here rather than by each caller, so there is one place
 * where "this is a data call" is decided.
 */
export async function json<T>(args: string[], stdin?: string): Promise<T> {
  const withJson = args.includes("--json") ? args : [...args, "--json"];
  const out = await text(withJson, stdin);
  try {
    return JSON.parse(out) as T;
  } catch {
    throw new Error(
      `ownbasectl ${withJson.join(" ")} did not print JSON. It said:\n${out.trim()}`,
    );
  }
}

/** One line of output from a command that is still running. */
export type StreamEvent =
  | { kind: "stdout"; line: string }
  | { kind: "stderr"; line: string }
  | { kind: "finished"; code: number }
  | { kind: "failed"; message: string };

export interface StreamHandle {
  /** Resolves with the exit code once the command ends. */
  done: Promise<number>;
  /** Kill the command. Used by the wizard's cancel button. */
  cancel: () => Promise<void>;
}

/**
 * Run a long command, calling `onEvent` as output arrives.
 *
 * This is for `create --wait` and `restore`, which spend several minutes of
 * real work. Streaming is the difference between a wizard that looks hung and
 * one that shows its work. Optional `stdin` is for secrets (never in argv) —
 * same rule as `raw`/`text`/`json`.
 */
export function stream(
  args: string[],
  onEvent: (event: StreamEvent) => void,
  stdin?: string,
): StreamHandle {
  const id = crypto.randomUUID();
  let settle: (code: number) => void;
  let fail: (err: unknown) => void;
  const done = new Promise<number>((resolve, reject) => {
    settle = resolve;
    fail = reject;
  });

  const channel = new Channel<StreamEvent>();
  channel.onmessage = (event) => {
    onEvent(event);
    if (event.kind === "finished") settle(event.code);
    if (event.kind === "failed") fail(new Error(event.message));
  };

  invoke("cli_stream", {
    id,
    args,
    stdin: stdin ?? null,
    onEvent: channel,
  }).catch(fail!);

  return {
    done,
    cancel: () => invoke("cli_cancel", { id }),
  };
}

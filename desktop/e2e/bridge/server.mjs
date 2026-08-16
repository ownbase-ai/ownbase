#!/usr/bin/env node
// Tier-B bridge: Playwright's IPC shim POSTs here; we spawn a real ownbasectl
// with an isolated HOME and stream results back.
//
// Start: node e2e/bridge/server.mjs
// Env:
//   E2E_BRIDGE_PORT   default 7391
//   E2E_OWNBASECTL    path to ownbasectl
//   E2E_HOME          isolated home (default: fresh mkdtemp under os.tmpdir())
//   E2E_BRIDGE_TOKEN  required on mutating routes (default: random, printed)

import { spawn, execFileSync } from "node:child_process";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { randomBytes } from "node:crypto";

const port = Number(process.env.E2E_BRIDGE_PORT || 7391);
const token = process.env.E2E_BRIDGE_TOKEN || randomBytes(16).toString("hex");
const home =
  process.env.E2E_HOME || mkdtempSync(join(tmpdir(), "ownbase-e2e-"));
mkdirSync(home, { recursive: true });

const root = join(dirname(fileURLToPath(import.meta.url)), "../..");
const tokenFile = join(home, ".bridge-token");
writeFileSync(tokenFile, token, { mode: 0o600 });

function resolveCtl() {
  if (process.env.E2E_OWNBASECTL) {
    if (!existsSync(process.env.E2E_OWNBASECTL)) {
      throw new Error(`E2E_OWNBASECTL not found: ${process.env.E2E_OWNBASECTL}`);
    }
    return process.env.E2E_OWNBASECTL;
  }
  try {
    const triple = execFileSync("rustc", ["-vV"], { encoding: "utf8" }).match(
      /^host: (.+)$/m,
    )?.[1];
    if (triple) {
      const p = join(root, "src-tauri", "binaries", `ownbasectl-${triple}`);
      if (existsSync(p)) return p;
    }
  } catch {
    /* fall through */
  }
  // Prefer PATH only when the binary is actually runnable.
  try {
    execFileSync("ownbasectl", ["version"], { stdio: "ignore" });
    return "ownbasectl";
  } catch {
    throw new Error(
      "ownbasectl not found. Build the sidecar (npm run sidecar) or set E2E_OWNBASECTL.",
    );
  }
}

let ctl;
try {
  ctl = resolveCtl();
} catch (err) {
  console.error(err instanceof Error ? err.message : err);
  process.exit(1);
}

const running = new Map();

function authorized(req) {
  const h = req.headers["x-e2e-token"];
  return typeof h === "string" && h === token;
}

function runCtl(args, stdin) {
  return new Promise((resolve, reject) => {
    const child = spawn(ctl, args, {
      env: { ...process.env, HOME: home },
      stdio: ["pipe", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => {
      stdout += d.toString();
    });
    child.stderr.on("data", (d) => {
      stderr += d.toString();
    });
    child.on("error", reject);
    child.stdin.on("error", () => {
      /* ignore EPIPE after early exit */
    });
    if (stdin != null) {
      child.stdin.write(stdin);
    }
    child.stdin.end();
    child.on("close", (code) => {
      resolve({ code: code ?? 1, stdout, stderr });
    });
  });
}

function streamCtl(id, args, stdin, send) {
  const child = spawn(ctl, args, {
    env: { ...process.env, HOME: home },
    stdio: ["pipe", "pipe", "pipe"],
  });
  running.set(id, child);

  const carry = { stdout: "", stderr: "" };
  const onChunk = (kind, buf) => {
    const text = carry[kind] + buf.toString();
    const parts = text.split(/\r?\n/);
    carry[kind] = parts.pop() ?? "";
    for (const line of parts) {
      if (line.length) send({ kind, line });
    }
  };
  child.stdout.on("data", (d) => onChunk("stdout", d));
  child.stderr.on("data", (d) => onChunk("stderr", d));
  child.on("error", (err) => {
    send({ kind: "failed", message: err.message });
  });
  child.stdin.on("error", () => {});
  if (stdin != null) child.stdin.write(stdin);
  child.stdin.end();
  child.on("close", (code) => {
    for (const kind of ["stdout", "stderr"]) {
      const tail = carry[kind].trim();
      if (tail) send({ kind, line: tail });
    }
    running.delete(id);
    send({ kind: "finished", code: code ?? 1 });
  });
}

async function shutdown() {
  for (const child of running.values()) {
    try {
      child.kill("SIGTERM");
    } catch {
      /* ignore */
    }
  }
  // Best-effort: stop a leaked credential agent from vault init.
  try {
    await runCtl(["agent", "stop"]);
  } catch {
    /* ignore */
  }
  try {
    await runCtl(["vault", "lock"]);
  } catch {
    /* ignore */
  }
  if (!process.env.E2E_HOME) {
    try {
      rmSync(home, { recursive: true, force: true });
    } catch {
      /* ignore */
    }
  }
}

const server = createServer(async (req, res) => {
  const origin = req.headers.origin;
  // Only same-machine Playwright (no origin) or localhost origins.
  if (origin && !/^https?:\/\/(127\.0\.0\.1|localhost)(:\d+)?$/.test(origin)) {
    res.writeHead(403);
    res.end("forbidden origin");
    return;
  }
  res.setHeader("Access-Control-Allow-Origin", origin || "http://127.0.0.1:5273");
  res.setHeader("Access-Control-Allow-Methods", "GET, POST, OPTIONS");
  res.setHeader("Access-Control-Allow-Headers", "content-type, x-e2e-token");
  if (req.method === "OPTIONS") {
    res.writeHead(204);
    res.end();
    return;
  }

  if (req.method === "GET" && req.url === "/health") {
    try {
      execFileSync(ctl, ["version"], { stdio: "ignore", env: { ...process.env, HOME: home } });
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify({ ok: true, ctl, home }));
    } catch (err) {
      res.writeHead(503, { "content-type": "application/json" });
      res.end(
        JSON.stringify({
          ok: false,
          error: err instanceof Error ? err.message : String(err),
        }),
      );
    }
    return;
  }

  if (req.method === "GET" && req.url === "/token") {
    // Localhost-only convenience for the Playwright fixture.
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify({ token }));
    return;
  }

  if (!authorized(req) && req.url !== "/health" && req.url !== "/token") {
    res.writeHead(401);
    res.end("unauthorized");
    return;
  }

  const body = await readBody(req);
  let payload = {};
  try {
    payload = body ? JSON.parse(body) : {};
  } catch {
    res.writeHead(400);
    res.end("bad json");
    return;
  }

  if (req.method === "POST" && req.url === "/run") {
    try {
      const result = await runCtl(payload.args ?? [], payload.stdin ?? null);
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify(result));
    } catch (err) {
      res.writeHead(500, { "content-type": "application/json" });
      res.end(
        JSON.stringify({
          code: 1,
          stdout: "",
          stderr: err instanceof Error ? err.message : String(err),
        }),
      );
    }
    return;
  }

  if (req.method === "POST" && req.url === "/stream") {
    res.writeHead(200, {
      "content-type": "text/event-stream",
      "cache-control": "no-cache",
      connection: "keep-alive",
    });
    const send = (event) => {
      res.write(`data: ${JSON.stringify(event)}\n\n`);
      if (event.kind === "finished" || event.kind === "failed") res.end();
    };
    streamCtl(payload.id ?? "x", payload.args ?? [], payload.stdin ?? null, send);
    req.on("close", () => {
      const child = running.get(payload.id);
      if (child) child.kill("SIGTERM");
    });
    return;
  }

  if (req.method === "POST" && req.url === "/cancel") {
    const child = running.get(payload.id);
    if (child) child.kill("SIGTERM");
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify({ ok: true }));
    return;
  }

  res.writeHead(404);
  res.end("not found");
});

function readBody(req) {
  return new Promise((resolve) => {
    const chunks = [];
    req.on("data", (c) => chunks.push(c));
    req.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
  });
}

server.listen(port, "127.0.0.1", () => {
  console.log(`e2e bridge on http://127.0.0.1:${port}`);
  console.log(`  ctl=${ctl}`);
  console.log(`  home=${home}`);
  console.log(`  token=${token}`);
});

for (const sig of ["SIGINT", "SIGTERM"]) {
  process.on(sig, () => {
    void shutdown().finally(() => process.exit(0));
  });
}

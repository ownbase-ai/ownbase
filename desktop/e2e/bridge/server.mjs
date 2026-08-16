#!/usr/bin/env node
// Tier-B bridge: Playwright's IPC shim POSTs here; we spawn a real ownbasectl
// with an isolated HOME/vault and stream results back.
//
// Start: node e2e/bridge/server.mjs
// Env:
//   E2E_BRIDGE_PORT   default 7391
//   E2E_OWNBASECTL    path to ownbasectl (default: ../src-tauri/binaries/ownbasectl-$triple
//                     or `ownbasectl` on PATH)
//   E2E_HOME          isolated home (default: os.tmpdir()/ownbase-e2e-home)

import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import { existsSync, mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname } from "node:path";
import { execSync } from "node:child_process";

const port = Number(process.env.E2E_BRIDGE_PORT || 7391);
const home = process.env.E2E_HOME || join(tmpdir(), "ownbase-e2e-home");
mkdirSync(home, { recursive: true });

const root = join(dirname(fileURLToPath(import.meta.url)), "../..");

function resolveCtl() {
  if (process.env.E2E_OWNBASECTL) return process.env.E2E_OWNBASECTL;
  try {
    const triple = execSync("rustc -vV", { encoding: "utf8" }).match(/^host: (.+)$/m)?.[1];
    if (triple) {
      const p = join(root, "src-tauri", "binaries", `ownbasectl-${triple}`);
      if (existsSync(p)) return p;
    }
  } catch {
    /* fall through */
  }
  return "ownbasectl";
}

const ctl = resolveCtl();
const running = new Map();

function runCtl(args, stdin) {
  return new Promise((resolve) => {
    const child = spawn(ctl, args, {
      env: { ...process.env, HOME: home, OWNBASE_HOME: home },
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
    env: { ...process.env, HOME: home, OWNBASE_HOME: home },
    stdio: ["pipe", "pipe", "pipe"],
  });
  running.set(id, child);

  // Carry partial lines across chunks so a progress line split mid-write is
  // not emitted as two truncated events.
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

const server = createServer(async (req, res) => {
  res.setHeader("Access-Control-Allow-Origin", "*");
  res.setHeader("Access-Control-Allow-Methods", "GET, POST, OPTIONS");
  res.setHeader("Access-Control-Allow-Headers", "content-type");
  if (req.method === "OPTIONS") {
    res.writeHead(204);
    res.end();
    return;
  }

  if (req.method === "GET" && req.url === "/health") {
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify({ ok: true, ctl, home, defaultHome: homedir() }));
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
    const result = await runCtl(payload.args ?? [], payload.stdin ?? null);
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify(result));
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
  console.log(`e2e bridge on http://127.0.0.1:${port} (ctl=${ctl} home=${home})`);
});

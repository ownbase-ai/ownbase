#!/usr/bin/env node
// Capture live ownbasectl --json documents into e2e/fixtures/captured/ so Tier-A
// fixtures can be refreshed from a real Base.
//
// Usage:
//   npm run e2e:capture -- <base>
//   E2E_OWNBASECTL=./src-tauri/binaries/ownbasectl-… npm run e2e:capture -- demo
//
// Requires an unlocked vault (ownbasectl vault unlock) on this machine.

import { spawnSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const base = process.argv[2];
if (!base) {
  console.error("usage: npm run e2e:capture -- <base>");
  process.exit(2);
}

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const outDir = join(root, "e2e", "fixtures", "captured");
mkdirSync(outDir, { recursive: true });

const ctl = process.env.E2E_OWNBASECTL || "ownbasectl";

function capture(name, args) {
  const r = spawnSync(ctl, [...args, "--json"], { encoding: "utf8" });
  if (r.status !== 0) {
    console.error(`fail: ${ctl} ${args.join(" ")} → ${r.status}`);
    console.error(r.stderr);
    return false;
  }
  const path = join(outDir, `${name}.json`);
  // Pretty-print if possible.
  let body = r.stdout;
  try {
    body = JSON.stringify(JSON.parse(r.stdout), null, 2) + "\n";
  } catch {
    /* leave raw */
  }
  writeFileSync(path, body);
  console.log(`wrote ${path}`);
  return true;
}

let ok = true;
ok = capture("vault-status", ["vault", "status"]) && ok;
ok = capture("list", ["list"]) && ok;
ok = capture(`checkup-${base}`, ["checkup", base]) && ok;
ok = capture("sessions-list", ["sessions", "list"]) && ok;
ok = capture("version", ["version"]) && ok;

process.exit(ok ? 0 : 1);

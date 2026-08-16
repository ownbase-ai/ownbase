#!/usr/bin/env node
// Capture live ownbasectl --json documents into e2e/fixtures/captured/ so
// Tier-A fixtures stay honest against real CLI shapes.
//
// Usage:
//   npm run e2e:capture -- <base>
//   E2E_OWNBASECTL=./src-tauri/binaries/ownbasectl-… npm run e2e:capture -- demo
//
// Requires an unlocked vault (ownbasectl vault unlock) on this machine.
// Output is redacted before write — never commit raw capture of a real Base.

import { spawnSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const outDir = join(root, "e2e", "fixtures", "captured");

/** RFC5737 / example.com stand-ins — safe to commit to a public repo. */
const REDACT = {
  ipv4: "203.0.113.10",
  ipv6: "2001:db8::10",
  host: "base.example.com",
  repo: "git@github.com:example/ownbase-config.git",
  serviceRepo: "git@github.com:example/web.git",
  domain: "web.example.com",
  vaultPath: "/tmp/ownbase-e2e/vault.kdbx",
  socket: "/tmp/ownbase-e2e/agent.sock",
  castDir: "/tmp/ownbase-e2e/sessions",
  user: "ubuntu",
  baseName: "demo",
};

const IPV4_RE = /\b(?:\d{1,3}\.){3}\d{1,3}\b/g;
const IPV6_RE = /\b(?:[0-9a-fA-F]{0,4}:){2,7}[0-9a-fA-F]{0,4}\b/g;
// git@host:org/repo(.git)?  or https://host/org/repo
const GIT_URL_RE =
  /(?:git@|https?:\/\/)[^\s"'\\]+(?:\.git)?/g;
const DOMAIN_RE =
  /\b(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:com|net|org|io|dev|local|internal)\b/gi;
const HOME_PATH_RE = /\/(?:Users|home)\/[^\s"'\\]+/g;
const SOCKET_RE = /\/[^\s"'\\]*agent[^\s"'\\]*\.sock/gi;
const CAST_PATH_RE = /\/[^\s"'\\]*sessions\/[^\s"'\\]+\.cast/g;

/**
 * Deep-walk a JSON value and scrub host-identifying strings. Structural keys
 * and booleans/numbers are left alone so shape tests still see real field sets.
 *
 * `path` is the key chain from the document root (array indices as strings).
 * Only BaseSummary rows use `name` for a Base — services, jobs, verify checks,
 * and core packages also have `name` and must keep their identities.
 */
function redactValue(value, key = "", path = []) {
  if (Array.isArray(value)) {
    return value.map((v, i) => redactValue(v, key, [...path, String(i)]));
  }
  if (value && typeof value === "object") {
    const out = {};
    for (const [k, v] of Object.entries(value)) {
      out[k] = redactValue(v, k, [...path, k]);
    }
    return out;
  }
  if (typeof value !== "string") return value;

  const k = key.toLowerCase();
  // Session / checkup Base pointer — always a Base name.
  if (k === "base") return REDACT.baseName;
  // `list --json` rows only: root array element whose key is `name`.
  // Do not rewrite services[].name, jobs[].name, checks[].name, packages[].name.
  if (
    k === "name" &&
    path.length === 2 &&
    /^\d+$/.test(path[0] ?? "") &&
    path[1] === "name"
  ) {
    return REDACT.baseName;
  }
  if (k === "host") return REDACT.host;
  if (k === "user" || k === "ssh_user") return REDACT.user;
  if (k === "vault_path") return REDACT.vaultPath;
  if (k === "ssh_agent_socket") return REDACT.socket;
  if (k === "cast_path") return `${REDACT.castDir}/20260815T120000Z-demo-abcd.cast`;
  if (k === "repo_url" || k === "config_repo_url") return REDACT.repo;
  if (k === "repo" && value.includes("git")) return REDACT.serviceRepo;
  if (k === "domain" || k === "domains") return REDACT.domain;
  // Session ids only — not b2_account_id / aws key material keys (those are
  // values under different key names and get path scrubbing below if needed).
  if (k === "id" && /^[A-Za-z0-9_-]{8,}$/.test(value)) {
    return "20260815T120000Z-demo-abcd";
  }

  let s = value;
  s = s.replace(CAST_PATH_RE, `${REDACT.castDir}/20260815T120000Z-demo-abcd.cast`);
  s = s.replace(SOCKET_RE, REDACT.socket);
  s = s.replace(HOME_PATH_RE, "/tmp/ownbase-e2e");
  s = s.replace(GIT_URL_RE, (m) =>
    m.includes("ownbase-config") || m.includes("config")
      ? REDACT.repo
      : REDACT.serviceRepo,
  );
  s = s.replace(IPV4_RE, REDACT.ipv4);
  s = s.replace(IPV6_RE, REDACT.ipv6);
  // Domains last so we don't mangle already-replaced example.com hosts.
  if (!s.includes("example.com") && !s.includes("example.org")) {
    s = s.replace(DOMAIN_RE, REDACT.host);
  }
  return s;
}

function selftest() {
  const sample = {
    findings: [],
    status: {
      services: [{ name: "web", domain: "app.realhost.io" }],
      jobs: [{ name: "migrate" }],
      security: {
        vulns: { host: { critical: 0 } },
      },
    },
    verify: { checks: [{ name: "restore", passed: true, detail: "ok" }] },
  };
  const list = [
    { name: "prod-mx", host: "185.1.2.3", base: "ignored" },
  ];
  const redList = redactValue(list);
  const redCheck = redactValue(sample);
  if (redList[0].name !== REDACT.baseName) {
    throw new Error(`list name: got ${redList[0].name}`);
  }
  if (redList[0].host !== REDACT.host && redList[0].host !== REDACT.ipv4) {
    // host key forces REDACT.host; IPs in other strings use ipv4
    throw new Error(`list host: got ${redList[0].host}`);
  }
  if (redCheck.status.services[0].name !== "web") {
    throw new Error(`service name rewritten: ${redCheck.status.services[0].name}`);
  }
  if (redCheck.status.jobs[0].name !== "migrate") {
    throw new Error(`job name rewritten: ${redCheck.status.jobs[0].name}`);
  }
  if (redCheck.verify.checks[0].name !== "restore") {
    throw new Error(`check name rewritten: ${redCheck.verify.checks[0].name}`);
  }
  console.log("capture.mjs selftest ok");
}

function main() {
  const base = process.argv[2];
  if (base === "--selftest") {
    selftest();
    return;
  }
  if (!base) {
    console.error("usage: npm run e2e:capture -- <base>");
    console.error("       npm run e2e:capture -- --selftest");
    process.exit(2);
  }

  mkdirSync(outDir, { recursive: true });
  const ctl = process.env.E2E_OWNBASECTL || "ownbasectl";

  function capture(name, args) {
    const r = spawnSync(ctl, args, { encoding: "utf8" });
    if (r.error) {
      console.error(`fail: ${ctl} ${args.join(" ")}: ${r.error.message}`);
      return false;
    }
    if (r.status !== 0) {
      console.error(`fail: ${ctl} ${args.join(" ")} → ${r.status}`);
      console.error(r.stderr);
      return false;
    }
    let body;
    try {
      body = JSON.stringify(redactValue(JSON.parse(r.stdout)), null, 2) + "\n";
    } catch {
      console.error(`fail: ${name}: stdout was not JSON`);
      return false;
    }
    const path = join(outDir, `${name}.json`);
    writeFileSync(path, body);
    console.log(`wrote ${path} (redacted)`);
    return true;
  }

  let ok = true;
  ok = capture("vault-status", ["vault", "status", "--json"]) && ok;
  ok = capture("list", ["list", "--json"]) && ok;
  ok = capture("checkup", ["checkup", base, "--json"]) && ok;
  ok = capture("sessions-list", ["sessions", "list", "--json"]) && ok;
  ok = capture("version", ["version", "--json"]) && ok;

  if (ok) {
    console.log(
      "\nReview the redacted files under e2e/fixtures/captured/ before committing.",
    );
  }
  process.exit(ok ? 0 : 1);
}

main();

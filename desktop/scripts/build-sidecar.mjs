#!/usr/bin/env node
// Builds ownbasectl from this checkout into src-tauri/binaries/, named the way
// Tauri expects a sidecar to be named: <name>-<rust-target-triple>.
//
// The app bundles the CLI rather than asking the user to install it separately,
// because the app is useless without it — every screen is a rendering of
// `ownbasectl --json`. Bundling also pins the two together, so the app can
// never be reading output from a CLI version that predates a field it wants.

import { execFileSync } from "node:child_process";
import { mkdirSync, existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const desktopDir = resolve(here, "..");
const repoRoot = resolve(desktopDir, "..");
const outDir = join(desktopDir, "src-tauri", "binaries");

function run(cmd, args, opts = {}) {
  return execFileSync(cmd, args, { encoding: "utf8", ...opts }).trim();
}

/** The Rust target triple Tauri will look for, e.g. aarch64-apple-darwin. */
function hostTriple() {
  if (process.env.OWNBASE_SIDECAR_TARGET) {
    return process.env.OWNBASE_SIDECAR_TARGET;
  }
  const info = run("rustc", ["-vV"]);
  const line = info.split("\n").find((l) => l.startsWith("host:"));
  if (!line) throw new Error("could not read the host target from `rustc -vV`");
  return line.split(":")[1].trim();
}

/** Map a Rust triple to the GOOS/GOARCH pair that produces the same binary. */
function goEnvFor(triple) {
  const [arch, , os] = triple.split("-");
  const goarch = { x86_64: "amd64", aarch64: "arm64" }[arch];
  const goos = { darwin: "darwin", linux: "linux", windows: "windows" }[os];
  if (!goarch || !goos) {
    throw new Error(`no Go target known for the Rust triple ${triple}`);
  }
  return { GOARCH: goarch, GOOS: goos };
}

const triple = hostTriple();
const { GOOS, GOARCH } = goEnvFor(triple);
const suffix = GOOS === "windows" ? ".exe" : "";
const outPath = join(outDir, `ownbasectl-${triple}${suffix}`);

mkdirSync(outDir, { recursive: true });

// Stamp the same version the CLI's own release builds use, so `ownbasectl
// version` inside the app is not the string "dev" in a shipped bundle.
const version = process.env.OWNBASE_VERSION ?? describeGitVersion();

function describeGitVersion() {
  try {
    return run("git", ["describe", "--tags", "--always", "--dirty"], {
      cwd: repoRoot,
    });
  } catch {
    return "dev";
  }
}

console.log(`building ownbasectl ${version} for ${triple} (${GOOS}/${GOARCH})`);

run(
  "go",
  [
    "build",
    "-trimpath",
    "-ldflags",
    `-s -w -X main.version=${version}`,
    "-o",
    outPath,
    "./cmd/ownbasectl",
  ],
  { cwd: repoRoot, env: { ...process.env, GOOS, GOARCH, CGO_ENABLED: "0" } },
);

if (!existsSync(outPath)) {
  throw new Error(`go build reported success but ${outPath} is missing`);
}
console.log(`wrote ${outPath}`);

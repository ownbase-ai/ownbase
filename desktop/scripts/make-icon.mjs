#!/usr/bin/env node
// Builds src-tauri/icon-source.png from the brand mark, then leaves the
// platform sizes to `tauri icon`.
//
// Source of truth is src-tauri/icon-mark.png (the OwnBase "O"). Place it on a
// full square white canvas — do not pre-draw rounded corners. macOS (and the
// other platform packagers) apply the squircle mask; baking one in makes the
// icon read larger than peers in the dock. Requires ImageMagick (`magick`).

import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SIZE = 1024;
/**
 * Longest edge of the mark. Kept under ~60% so the glyph has similar internal
 * padding to Notion / Slack rather than filling the tile edge-to-edge.
 */
const MARK = 560;
const BG = "#ffffff";

const here = dirname(fileURLToPath(import.meta.url));
const tauri = resolve(here, "..", "src-tauri");
const mark = join(tauri, "icon-mark.png");
const out = join(tauri, "icon-source.png");

if (!existsSync(mark)) {
  console.error(`missing brand mark: ${mark}`);
  process.exit(1);
}

const check = spawnSync("magick", ["-version"], { encoding: "utf8" });
if (check.error || check.status !== 0) {
  console.error("ImageMagick `magick` is required on PATH to rebuild the icon.");
  console.error("  brew install imagemagick");
  process.exit(1);
}

const args = [
  "-size",
  `${SIZE}x${SIZE}`,
  `xc:${BG}`,
  "(",
  mark,
  "-resize",
  `${MARK}x${MARK}`,
  ")",
  "-gravity",
  "center",
  "-compose",
  "over",
  "-composite",
  "-depth",
  "8",
  out,
];

const result = spawnSync("magick", args, { encoding: "utf8" });
if (result.status !== 0) {
  console.error(result.stderr || result.stdout || "magick failed");
  process.exit(result.status ?? 1);
}

console.log(`wrote ${out} (${SIZE}x${SIZE}, full square ${BG}, mark ${MARK}px)`);
console.log(`next: npx tauri icon ${join("src-tauri", "icon-source.png")}`);

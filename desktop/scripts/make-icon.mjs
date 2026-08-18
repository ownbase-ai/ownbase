#!/usr/bin/env node
// Builds src-tauri/icon-source.png from the brand mark, then leaves the
// platform sizes to `tauri icon`.
//
// Source of truth is src-tauri/icon-mark.png (the OwnBase "O"). We place it on
// a transparent 1024² canvas at ~78% of the side so macOS's dock mask and
// 16‑px favicons keep a margin. Requires ImageMagick (`magick` on PATH).

import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SIZE = 1024;
/** Longest edge of the mark inside the canvas. ~11% padding each side. */
const MARK = 800;

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
  "xc:none",
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
  // 8-bit keeps the committed PNG small; the mark has no HDR need.
  "-depth",
  "8",
  out,
];

const result = spawnSync("magick", args, { encoding: "utf8" });
if (result.status !== 0) {
  console.error(result.stderr || result.stdout || "magick failed");
  process.exit(result.status ?? 1);
}

console.log(`wrote ${out} (${SIZE}x${SIZE}, mark ${MARK}px)`);
console.log(`next: npx tauri icon ${join("src-tauri", "icon-source.png")}`);

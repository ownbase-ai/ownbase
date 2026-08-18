#!/usr/bin/env node
// Builds src-tauri/icon-source.png from the brand mark, then leaves the
// platform sizes to `tauri icon`.
//
// Source of truth is src-tauri/icon-mark.png (the OwnBase "O"). We place it on
// a dark rounded-rect tile that matches the app chrome, full-bleed at 1024² so
// it reads like other macOS dock icons (Notion, Settings, …) rather than a bare
// circle. Requires ImageMagick (`magick` on PATH).

import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SIZE = 1024;
/** Longest edge of the mark inside the tile. */
const MARK = 620;
/** Continuous-corner radius (~22% of side). */
const CORNER = 230;
/** Near-black app chrome (zinc-950-ish). */
const BG = "#0b0d10";

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

// Tile = solid fill masked to a rounded rect, then the mark centered on top.
const args = [
  "-size",
  `${SIZE}x${SIZE}`,
  "xc:none",
  "(",
  "-size",
  `${SIZE}x${SIZE}`,
  `xc:${BG}`,
  ")",
  "(",
  "-size",
  `${SIZE}x${SIZE}`,
  "xc:none",
  "-fill",
  "white",
  "-draw",
  `roundrectangle 0,0 ${SIZE - 1},${SIZE - 1} ${CORNER},${CORNER}`,
  ")",
  "-compose",
  "copyopacity",
  "-composite",
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

console.log(`wrote ${out} (${SIZE}x${SIZE}, tile ${BG}, mark ${MARK}px)`);
console.log(`next: npx tauri icon ${join("src-tauri", "icon-source.png")}`);

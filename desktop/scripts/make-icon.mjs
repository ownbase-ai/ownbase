#!/usr/bin/env node
// Builds src-tauri/icon-source.png from the brand mark, then leaves the
// platform sizes to `tauri icon`.
//
// Source of truth is src-tauri/icon-mark.png (the OwnBase "O").
//
// Layout matches common Electron/macOS dock icons (e.g. Slack): a white
// squircle centered on a transparent 1024² canvas at ~82% of the side, so the
// dock size lines up with neighbors. Full-bleed tiles read oversized. Requires
// ImageMagick (`magick` on PATH).

import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SIZE = 1024;
/** Squircle side as a fraction of the canvas (Slack ≈ 420/512). */
const TILE_FRAC = 420 / 512;
const TILE = Math.round(SIZE * TILE_FRAC);
const PAD = Math.round((SIZE - TILE) / 2);
/** Corner radius ≈ 22.4% of the tile (Apple continuous corner). */
const CORNER = Math.round(TILE * 0.2237);
/** Mark longest edge relative to the tile. */
const MARK = Math.round(TILE * 0.62);
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

const x0 = PAD;
const y0 = PAD;
const x1 = PAD + TILE - 1;
const y1 = PAD + TILE - 1;

// Transparent canvas → white squircle at PAD inset → mark centered.
const args = [
  "-size",
  `${SIZE}x${SIZE}`,
  "xc:none",
  "-fill",
  BG,
  "-draw",
  `roundrectangle ${x0},${y0} ${x1},${y1} ${CORNER},${CORNER}`,
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

console.log(
  `wrote ${out} (${SIZE}x${SIZE}, tile ${TILE}px r=${CORNER}, pad ${PAD}px, mark ${MARK}px)`,
);
console.log(`next: npx tauri icon ${join("src-tauri", "icon-source.png")}`);

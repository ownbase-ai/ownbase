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
import { existsSync, mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SIZE = 1024;
/** Squircle side as a fraction of the canvas (Slack ≈ 420/512). */
const TILE_FRAC = 420 / 512;
const TILE = Math.round(SIZE * TILE_FRAC);
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

const tmp = mkdtempSync(join(tmpdir(), "ownbase-icon-"));
const svgPath = join(tmp, "tile.svg");
try {
  writeFileSync(
    svgPath,
    `<svg xmlns="http://www.w3.org/2000/svg" width="${TILE}" height="${TILE}">` +
      `<rect width="${TILE}" height="${TILE}" rx="${CORNER}" ry="${CORNER}" fill="${BG}"/>` +
      `</svg>\n`,
  );

  // Transparent canvas → centered squircle → centered mark.
  const args = [
    "-size",
    `${SIZE}x${SIZE}`,
    "xc:none",
    "(",
    "-background",
    "none",
    svgPath,
    ")",
    "-gravity",
    "center",
    "-compose",
    "over",
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
} finally {
  rmSync(tmp, { recursive: true, force: true });
}

const pad = Math.round((SIZE - TILE) / 2);
console.log(
  `wrote ${out} (${SIZE}x${SIZE}, tile ${TILE}px r=${CORNER}, pad ${pad}px, mark ${MARK}px)`,
);
console.log(`next: npx tauri icon ${join("src-tauri", "icon-source.png")}`);

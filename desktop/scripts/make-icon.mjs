#!/usr/bin/env node
// Builds src-tauri/icon-source.png from the brand mark, then leaves the
// platform sizes to `tauri icon`.
//
// Source of truth is src-tauri/icon-mark.png (the OwnBase "O").
//
// The tile is a white squircle with transparent corners, full-bleed to the
// canvas edges on the flat sides. We bake the shape in (rather than shipping a
// sharp square) so the dock looks right even when macOS does not re-mask the
// asset — which is what made the full-square version read as a big white box.
// Requires ImageMagick (`magick` on PATH).

import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SIZE = 1024;
/** Longest edge of the mark — roomy, Notion-like padding inside the tile. */
const MARK = 540;
/**
 * Corner radius as a fraction of side. ~22.4% matches Apple's continuous
 * corner on a 1024 macOS app icon grid.
 */
const CORNER = Math.round(SIZE * 0.2237);
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
  // SVG roundrect rasterizes cleaner edge AA than magick -draw.
  writeFileSync(
    svgPath,
    `<svg xmlns="http://www.w3.org/2000/svg" width="${SIZE}" height="${SIZE}">` +
      `<rect width="${SIZE}" height="${SIZE}" rx="${CORNER}" ry="${CORNER}" fill="${BG}"/>` +
      `</svg>\n`,
  );

  const args = [
    "-background",
    "none",
    svgPath,
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

console.log(`wrote ${out} (${SIZE}x${SIZE}, white squircle r=${CORNER}, mark ${MARK}px)`);
console.log(`next: npx tauri icon ${join("src-tauri", "icon-source.png")}`);

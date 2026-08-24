#!/usr/bin/env node
/**
 * Regenerate public/og.png and the favicon set from the brand mark + wordmark.
 * Requires ImageMagick (`magick` on PATH).
 *
 *   node scripts/make-og.mjs
 *
 * Favicons are a white rounded squircle with the brand mark, transparent
 * outside the squircle. Transparent pixels keep white RGB (not black) so
 * browsers that mishandle premultiplied alpha don't paint black corners.
 */
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
// Prefer the full-res brand mark from the desktop app; the copy under
// src/assets/ is downscaled for the in-page diamond.
const markFull = join(root, "../desktop/src-tauri/icon-mark.png");
const mark = existsSync(markFull)
  ? markFull
  : join(root, "src/assets/icon-mark.png");
const wordmark = join(root, "src/assets/wordmark.png");
const out = join(root, "public");
mkdirSync(out, { recursive: true });

function magick(args) {
  execFileSync("magick", args, { stdio: "inherit" });
}

// Apple-style continuous corner ≈ 22.37% of side (matches desktop make-icon).
const SIDE = 512;
const RADIUS = Math.round(SIDE * 0.2237);
const MARK_SIZE = Math.round(SIDE * 0.7);

const opaque = "/tmp/ownbase-site-icon-opaque.png";
const mask = "/tmp/ownbase-site-icon-mask.png";

// Opaque white tile + centered mark (RGB only).
magick([
  "-size",
  `${SIDE}x${SIDE}`,
  "xc:#FFFFFF",
  "(",
  mark,
  "-resize",
  `${MARK_SIZE}x${MARK_SIZE}`,
  ")",
  "-gravity",
  "center",
  "-compose",
  "over",
  "-composite",
  opaque,
]);

// Squircle mask: white inside, black outside.
magick([
  "-size",
  `${SIDE}x${SIDE}`,
  "xc:black",
  "-fill",
  "white",
  "-draw",
  `roundrectangle 0,0 ${SIDE - 1},${SIDE - 1} ${RADIUS},${RADIUS}`,
  mask,
]);

function writeSized(size, dest) {
  const op = `/tmp/ownbase-op-${size}.png`;
  const mk = `/tmp/ownbase-mk-${size}.png`;
  magick([opaque, "-resize", `${size}x${size}`, op]);
  magick([mask, "-resize", `${size}x${size}`, mk]);
  // Recombine so corners are #FFFFFF with alpha 0 (not #00000000).
  magick([
    op,
    mk,
    "-alpha",
    "off",
    "-compose",
    "CopyOpacity",
    "-composite",
    `PNG32:${dest}`,
  ]);
}

for (const [size, name] of [
  [32, "favicon-32.png"],
  [16, "favicon-16.png"],
  [180, "apple-touch-icon.png"],
  [192, "icon-192.png"],
  [512, "icon-512.png"],
]) {
  writeSized(size, join(out, name));
}

// ICO: build each size with correct alpha, then pack.
const icoParts = [];
for (const size of [64, 48, 32, 16]) {
  const p = `/tmp/ownbase-ico-${size}.png`;
  writeSized(size, p);
  icoParts.push(p);
}
magick([...icoParts, join(out, "favicon.ico")]);

const font = "/System/Library/Fonts/Supplemental/Arial.ttf";
magick([
  "-size",
  "1200x630",
  "xc:#FFFFFF",
  "(",
  wordmark,
  "-resize",
  "560x",
  ")",
  "-gravity",
  "center",
  "-geometry",
  "+0-50",
  "-compose",
  "over",
  "-composite",
  "-font",
  font,
  "-fill",
  "#0F172A",
  "-pointsize",
  "28",
  "-gravity",
  "center",
  "-annotate",
  "+0+70",
  "Build faster. Own everything.",
  "-fill",
  "#004EDF",
  "-pointsize",
  "20",
  "-annotate",
  "+0+120",
  "ownbase.ai",
  join(out, "og.png"),
]);

console.log("==> wrote rounded favicons + og.png under public/");

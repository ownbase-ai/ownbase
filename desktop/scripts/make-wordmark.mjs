#!/usr/bin/env node
// Builds src/assets/wordmark.png from a light-background brand lockup.
//
// Source is a full-bleed PNG of the OwnBase wordmark (mark + "OwnBase" text)
// on a near-white field. We trim to the ink bbox, knock white to alpha so the
// result sits on any surface, and downscale to a retina-friendly size for the
// sidebar's h-7 render height. Requires ImageMagick (`magick` on PATH).
//
// Usage:
//   node scripts/make-wordmark.mjs [source.png]
// Default source: the path passed, or OWNBASE_WORDMARK_SRC env, or fail.

import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

/** Longest edge of the output. ~7× the 28 px sidebar render height. */
const LONG_EDGE = 800;

const here = dirname(fileURLToPath(import.meta.url));
const out = resolve(here, "..", "src", "assets", "wordmark.png");
const src =
  process.argv[2] ||
  process.env.OWNBASE_WORDMARK_SRC ||
  "";

if (!src) {
  console.error("usage: node scripts/make-wordmark.mjs <source.png>");
  process.exit(1);
}
if (!existsSync(src)) {
  console.error(`missing source: ${src}`);
  process.exit(1);
}

const check = spawnSync("magick", ["-version"], { encoding: "utf8" });
if (check.error || check.status !== 0) {
  console.error("ImageMagick `magick` is required on PATH to rebuild the wordmark.");
  console.error("  brew install imagemagick");
  process.exit(1);
}

// Trim whitespace → knock near-white to alpha → resize → write PNG.
// Fuzz is generous enough to catch the soft AI-generated fringe without
// eating into the anti-aliased blue of the mark.
const args = [
  src,
  "-fuzz",
  "12%",
  "-trim",
  "+repage",
  "-fuzz",
  "12%",
  "-transparent",
  "white",
  "-resize",
  `${LONG_EDGE}x${LONG_EDGE}>`,
  "-depth",
  "8",
  out,
];

const result = spawnSync("magick", args, { encoding: "utf8" });
if (result.status !== 0) {
  console.error(result.stderr || result.stdout || "magick failed");
  process.exit(result.status ?? 1);
}

const info = spawnSync("magick", ["identify", "-format", "%wx%h", out], {
  encoding: "utf8",
});
console.log(`wrote ${out} (${(info.stdout || "").trim()})`);

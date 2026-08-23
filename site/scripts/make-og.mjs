#!/usr/bin/env node
/**
 * Regenerate public/og.png and the favicon set from the brand mark + wordmark.
 * Requires ImageMagick (`magick` on PATH).
 *
 *   node scripts/make-og.mjs
 */
import { execFileSync } from "node:child_process";
import { mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const mark = join(root, "src/assets/icon-mark.png");
const wordmark = join(root, "src/assets/wordmark.png");
const out = join(root, "public");
mkdirSync(out, { recursive: true });

function magick(args) {
  execFileSync("magick", args, { stdio: "inherit" });
}

const icon512 = "/tmp/ownbase-site-icon-512.png";
magick([
  "-size", "512x512", "xc:none",
  "(", "-size", "512x512", "xc:white",
  "-draw", "roundrectangle 0,0 511,511 114,114", ")",
  "-compose", "over", "-composite",
  "(", mark, "-resize", "360x360", ")",
  "-gravity", "center", "-compose", "over", "-composite",
  icon512,
]);

for (const [size, name] of [
  [32, "favicon-32.png"],
  [16, "favicon-16.png"],
  [180, "apple-touch-icon.png"],
  [192, "icon-192.png"],
  [512, "icon-512.png"],
]) {
  magick([icon512, "-resize", `${size}x${size}`, join(out, name)]);
}
magick([icon512, "-define", "icon:auto-resize=64,48,32,16", join(out, "favicon.ico")]);

const font = "/System/Library/Fonts/Supplemental/Arial.ttf";
magick([
  "-size", "1200x630", "xc:#FFFFFF",
  "(", wordmark, "-resize", "560x", ")",
  "-gravity", "center", "-geometry", "+0-50",
  "-compose", "over", "-composite",
  "-font", font, "-fill", "#0F172A", "-pointsize", "28",
  "-gravity", "center", "-annotate", "+0+70",
  "Build faster. Own everything.",
  "-fill", "#004EDF", "-pointsize", "20",
  "-annotate", "+0+120", "ownbase.ai",
  join(out, "og.png"),
]);

console.log("==> wrote favicons + og.png under public/");

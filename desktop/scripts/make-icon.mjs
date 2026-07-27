#!/usr/bin/env node
// Draws the source app icon and writes it as a PNG, then lets `tauri icon`
// derive every platform size from it.
//
// The mark is generated rather than committed as a binary so it stays reviewable
// and reproducible: the geometry below *is* the artwork, and changing it is a
// diff rather than an opaque file swap. It is a ring — the "O" of OwnBase — on a
// rounded square, which is what reads at 16 pixels.

import { deflateSync } from "node:zlib";
import { writeFileSync, mkdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SIZE = 1024;
const BG = [11, 13, 16, 255]; // near-black, matches the app chrome
const FG = [52, 211, 153, 255]; // emerald-400
const CORNER = SIZE * 0.22;
const RING_OUTER = SIZE * 0.315;
const RING_INNER = SIZE * 0.185;
/** Gap at the bottom of the ring, so the mark is a ring and not just an O. */
const GAP_HALF_ANGLE = 0.42;

/** Coverage of a pixel by the rounded square, antialiased by supersampling. */
function roundedSquareAt(x, y) {
  const inset = SIZE * 0.055;
  const min = inset;
  const max = SIZE - inset;
  const r = CORNER;
  // Distance to the rounded rectangle: clamp into the inner box, then measure.
  const cx = Math.min(Math.max(x, min + r), max - r);
  const cy = Math.min(Math.max(y, min + r), max - r);
  const dx = x - cx;
  const dy = y - cy;
  if (x >= min + r && x <= max - r) return y >= min && y <= max ? 1 : 0;
  if (y >= min + r && y <= max - r) return x >= min && x <= max ? 1 : 0;
  return Math.hypot(dx, dy) <= r ? 1 : 0;
}

function ringAt(x, y) {
  const dx = x - SIZE / 2;
  const dy = y - SIZE / 2;
  const dist = Math.hypot(dx, dy);
  if (dist > RING_OUTER || dist < RING_INNER) return 0;
  // Angle measured from straight down, so the gap sits at the base of the mark.
  const angle = Math.atan2(dx, dy);
  return Math.abs(angle) < GAP_HALF_ANGLE ? 0 : 1;
}

const SAMPLES = 4; // 4x4 supersampling is plenty at this resolution
const pixels = Buffer.alloc(SIZE * SIZE * 4);

for (let y = 0; y < SIZE; y++) {
  for (let x = 0; x < SIZE; x++) {
    let bg = 0;
    let fg = 0;
    for (let sy = 0; sy < SAMPLES; sy++) {
      for (let sx = 0; sx < SAMPLES; sx++) {
        const px = x + (sx + 0.5) / SAMPLES;
        const py = y + (sy + 0.5) / SAMPLES;
        bg += roundedSquareAt(px, py);
        fg += ringAt(px, py);
      }
    }
    const total = SAMPLES * SAMPLES;
    const bgA = bg / total;
    const fgA = (fg / total) * bgA; // the ring is clipped to the square
    const offset = (y * SIZE + x) * 4;
    for (let c = 0; c < 3; c++) {
      pixels[offset + c] = Math.round(BG[c] * (1 - fgA) + FG[c] * fgA);
    }
    pixels[offset + 3] = Math.round(255 * bgA);
  }
}

// --- Minimal PNG encoder (RGBA, no interlace, one IDAT) ---------------------

function chunk(type, data) {
  const length = Buffer.alloc(4);
  length.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body));
  return Buffer.concat([length, body, crc]);
}

const crcTable = (() => {
  const table = new Int32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    table[n] = c;
  }
  return table;
})();

function crc32(buf) {
  let c = -1;
  for (const byte of buf) c = crcTable[(c ^ byte) & 0xff] ^ (c >>> 8);
  return (c ^ -1) >>> 0;
}

const ihdr = Buffer.alloc(13);
ihdr.writeUInt32BE(SIZE, 0);
ihdr.writeUInt32BE(SIZE, 4);
ihdr[8] = 8; // bit depth
ihdr[9] = 6; // colour type: RGBA
// 10..12 are compression, filter, and interlace: all zero.

// Each scanline is prefixed with its filter type; 0 means "store as-is".
const rows = Buffer.alloc(SIZE * (SIZE * 4 + 1));
for (let y = 0; y < SIZE; y++) {
  const dst = y * (SIZE * 4 + 1);
  rows[dst] = 0;
  pixels.copy(rows, dst + 1, y * SIZE * 4, (y + 1) * SIZE * 4);
}

const png = Buffer.concat([
  Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
  chunk("IHDR", ihdr),
  chunk("IDAT", deflateSync(rows, { level: 9 })),
  chunk("IEND", Buffer.alloc(0)),
]);

const here = dirname(fileURLToPath(import.meta.url));
const out = resolve(here, "..", "src-tauri", "icon-source.png");
mkdirSync(dirname(out), { recursive: true });
writeFileSync(out, png);
console.log(`wrote ${out} (${SIZE}x${SIZE})`);
console.log(`next: npx tauri icon ${join("src-tauri", "icon-source.png")}`);

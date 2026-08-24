/**
 * OwnBase shared Tailwind preset.
 *
 * One source of truth for the brand palette, mono stack, and fade-in
 * animation used by both the desktop app (desktop/) and the marketing
 * site (site/). CSS custom properties are emitted via a plugin so the
 * RGB triplets live next to the Tailwind mapping instead of in a
 * separate CSS file each consumer has to keep in sync.
 *
 * App-only chrome (user-select: none, overflow: hidden, scrollbar
 * styling, --ownbase-mono for the asciicast player) stays in
 * desktop/src/index.css — those rules are hostile to a webpage and
 * must not leak into the shared layer.
 *
 * Brand accent is #004EDF.
 *
 * No import of tailwindcss itself: both consumers already depend on it,
 * and a bare plugin function is a valid Tailwind plugin (no need for
 * the tailwindcss/plugin helper). That keeps this file resolvable from
 * either package without path-hacking node_modules.
 */

function withAlpha(variable) {
  return `rgb(var(${variable}) / <alpha-value>)`;
}

/** @type {import('tailwindcss').Config} */
export default {
  theme: {
    extend: {
      colors: {
        canvas: withAlpha("--color-canvas"),
        surface: {
          DEFAULT: withAlpha("--color-surface"),
          muted: withAlpha("--color-surface-muted"),
          sunken: withAlpha("--color-surface-sunken"),
        },
        line: {
          DEFAULT: withAlpha("--color-line"),
          strong: withAlpha("--color-line-strong"),
        },
        fg: {
          DEFAULT: withAlpha("--color-fg"),
          muted: withAlpha("--color-fg-muted"),
          subtle: withAlpha("--color-fg-subtle"),
          faint: withAlpha("--color-fg-faint"),
        },
        accent: {
          DEFAULT: withAlpha("--color-accent"),
          hover: withAlpha("--color-accent-hover"),
          soft: withAlpha("--color-accent-soft"),
          line: withAlpha("--color-accent-line"),
          fg: withAlpha("--color-accent-fg"),
        },
        good: {
          DEFAULT: withAlpha("--color-good"),
          soft: withAlpha("--color-good-soft"),
          line: withAlpha("--color-good-line"),
          fg: withAlpha("--color-good-fg"),
        },
        warn: {
          DEFAULT: withAlpha("--color-warn"),
          soft: withAlpha("--color-warn-soft"),
          line: withAlpha("--color-warn-line"),
          fg: withAlpha("--color-warn-fg"),
        },
        bad: {
          DEFAULT: withAlpha("--color-bad"),
          soft: withAlpha("--color-bad-soft"),
          line: withAlpha("--color-bad-line"),
          fg: withAlpha("--color-bad-fg"),
        },
        info: {
          DEFAULT: withAlpha("--color-info"),
          soft: withAlpha("--color-info-soft"),
          line: withAlpha("--color-info-line"),
          fg: withAlpha("--color-info-fg"),
        },
      },
      fontFamily: {
        mono: [
          "ui-monospace",
          "SFMono-Regular",
          "SF Mono",
          "Menlo",
          "Consolas",
          "monospace",
        ],
      },
      keyframes: {
        "fade-in": {
          from: { opacity: "0", transform: "translateY(4px)" },
          to: { opacity: "1", transform: "none" },
        },
      },
      animation: {
        "fade-in": "fade-in 180ms ease-out",
      },
    },
  },
  plugins: [
    function ownbaseTokens({ addBase }) {
      addBase({
        ":root": {
          "color-scheme": "light",

          /* Surfaces */
          "--color-canvas": "255 255 255",
          "--color-surface": "255 255 255",
          "--color-surface-muted": "250 250 250",
          "--color-surface-sunken": "244 245 247",

          /* Lines */
          "--color-line": "229 231 235",
          "--color-line-strong": "212 216 222",

          /* Foreground */
          "--color-fg": "15 23 42",
          "--color-fg-muted": "71 85 105",
          "--color-fg-subtle": "100 116 139",
          "--color-fg-faint": "148 163 184",

          /* Accent — brand blue #004EDF */
          "--color-accent": "0 78 223",
          "--color-accent-hover": "0 64 184",
          "--color-accent-soft": "237 243 255",
          "--color-accent-line": "191 214 255",
          "--color-accent-fg": "0 64 184",

          /* Status: good (green stays green — not the brand accent) */
          "--color-good": "34 197 94",
          "--color-good-soft": "240 253 244",
          "--color-good-line": "187 247 208",
          "--color-good-fg": "22 163 74",

          /* Status: warn */
          "--color-warn": "245 158 11",
          "--color-warn-soft": "255 251 235",
          "--color-warn-line": "253 230 138",
          "--color-warn-fg": "180 83 9",

          /* Status: bad */
          "--color-bad": "239 68 68",
          "--color-bad-soft": "254 242 242",
          "--color-bad-line": "254 202 202",
          "--color-bad-fg": "185 28 28",

          /* Status: info (brand blue) */
          "--color-info": "0 78 223",
          "--color-info-soft": "237 243 255",
          "--color-info-line": "191 214 255",
          "--color-info-fg": "0 64 184",
        },
      });
    },
  ],
};


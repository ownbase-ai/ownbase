/** @type {import('tailwindcss').Config} */
function withAlpha(variable) {
  return `rgb(var(${variable}) / <alpha-value>)`;
}

export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
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
  plugins: [],
};

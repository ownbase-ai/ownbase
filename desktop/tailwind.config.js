/** @type {import('tailwindcss').Config} */
import ownbase from "../shared/theme/preset.mjs";

export default {
  presets: [ownbase],
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {},
  },
  plugins: [],
};

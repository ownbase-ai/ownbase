/** @type {import('tailwindcss').Config} */
import ownbase from "../shared/theme/preset.mjs";

export default {
  presets: [ownbase],
  content: ["./src/**/*.{astro,html,js,jsx,md,mdx,svelte,ts,tsx,vue}"],
  theme: {
    extend: {
      fontFamily: {
        // Display serif for the big headlines. Body stays the system stack
        // from the shared preset so the site and the app share one face for
        // prose and mono.
        display: [
          '"Playfair Display Variable"',
          "Playfair Display",
          "Georgia",
          "Times New Roman",
          "serif",
        ],
      },
      maxWidth: {
        content: "72rem",
      },
    },
  },
  plugins: [],
};

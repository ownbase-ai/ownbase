import { defineConfig } from "astro/config";
import tailwind from "@astrojs/tailwind";

// Static marketing site for ownbase.ai.
// build.format: 'file' emits comparison.html (not comparison/index.html) so a
// future second page resolves on S3+CloudFront without a CloudFront Function.
export default defineConfig({
  site: "https://ownbase.ai",
  output: "static",
  build: {
    format: "file",
  },
  integrations: [
    tailwind({
      // We own base styles in src/styles/global.css so the shared preset's
      // tokens and the site's body rules stay under one roof.
      applyBaseStyles: false,
    }),
  ],
});

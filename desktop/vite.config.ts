import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Tauri serves the dev build over a fixed port and needs a strict failure if
// that port is taken, so the app never silently connects to something else.
const host = process.env.TAURI_DEV_HOST;

export default defineConfig({
  plugins: [react()],
  clearScreen: false,
  server: {
    port: 5273,
    strictPort: true,
    host: host || false,
    hmr: host ? { protocol: "ws", host, port: 5274 } : undefined,
    watch: { ignored: ["**/src-tauri/**"] },
  },
  build: {
    // Matches the Rust/WebKit versions Tauri 2 ships against.
    target: "es2022",
    sourcemap: true,
  },
  // Unit tests only. Playwright owns e2e/tests and must not be collected by vitest.
  // fixtures-shape.test.ts imports from e2e/fixtures (data only, no Playwright).
  test: {
    include: ["src/**/*.{test,spec}.{ts,tsx}"],
    exclude: ["e2e/tests/**", "e2e/tests-real/**", "node_modules/**", "src-tauri/**"],
  },
});

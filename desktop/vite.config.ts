import { defineConfig } from "vite";
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
});

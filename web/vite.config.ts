import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

const docsDir = fileURLToPath(new URL("../docs", import.meta.url));
// Dev proxy target: the Caddy edge from docker compose by default.
const apiTarget = process.env.API_TARGET ?? "http://localhost:5560";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@docs": docsDir },
  },
  build: {
    // CSP forbids inline scripts/styles; keep everything in /assets files.
    assetsInlineLimit: 0,
    modulePreload: { polyfill: false },
    sourcemap: false,
  },
  server: {
    port: 5173,
    fs: { allow: [".", docsDir] },
    proxy: {
      "/v1": { target: apiTarget, ws: true, changeOrigin: false },
      "/healthz": { target: apiTarget },
    },
  },
  test: {
    include: ["src/**/*.spec.ts"],
    environment: "node",
  },
});

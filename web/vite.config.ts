import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { build } from "vite";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";

const docsDir = fileURLToPath(new URL("../docs", import.meta.url));
const swEntry = fileURLToPath(new URL("./src/sw/sw.ts", import.meta.url));
const swRoutes = fileURLToPath(new URL("./src/sw/routes.ts", import.meta.url));
// Dev proxy target: the Caddy edge from docker compose by default.
const apiTarget = process.env.API_TARGET ?? "http://localhost:5560";

// Builds src/sw/sw.ts into dist/sw.js (classic script, fixed name) with the
// build's precache list and an id that changes whenever any output does.
function swPlugin(): Plugin {
  return {
    name: "remotectl-sw",
    apply: "build",
    // After Vite's own plugins, so index.html is in the bundle.
    enforce: "post",
    async generateBundle(_opts, bundle) {
      const files = Object.keys(bundle).sort();
      if (!files.includes("index.html")) this.error("index.html is not in the bundle yet");
      const assets = files.filter((f) => f.startsWith("assets/")).map((f) => `/${f}`);
      const hash = createHash("sha256");
      hash.update(readFileSync(swEntry));
      hash.update(readFileSync(swRoutes));
      for (const f of files) {
        const out = bundle[f];
        if (!out) continue;
        hash.update(f);
        hash.update(out.type === "chunk" ? out.code : out.source);
      }
      const buildId = hash.digest("hex").slice(0, 16);
      const result = await build({
        configFile: false,
        logLevel: "warn",
        define: {
          __BUILD_ID__: JSON.stringify(buildId),
          __PRECACHE__: JSON.stringify(["/", ...assets]),
        },
        build: {
          write: false,
          emptyOutDir: false,
          minify: true,
          sourcemap: false,
          lib: { entry: swEntry, formats: ["iife"], name: "remotectlSw", fileName: () => "sw.js" },
        },
      });
      const outputs = Array.isArray(result) ? result : [result];
      for (const out of outputs) {
        if (!("output" in out)) continue;
        for (const o of out.output) {
          if (o.type === "chunk") {
            this.emitFile({ type: "asset", fileName: "sw.js", source: o.code });
            return;
          }
        }
      }
      this.error("service worker build produced no output");
    },
  };
}

export default defineConfig({
  plugins: [react(), tailwindcss(), swPlugin()],
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

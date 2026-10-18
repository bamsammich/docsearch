import { writeFileSync } from "node:fs";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { defineConfig } from "vitest/config";

const outDir = "../internal/webui/dist";

// go:embed needs dist/ to hold a file even in a checkout that never built
// the app, so a build puts back the placeholder its emptying removed.
const keepPlaceholder = {
  name: "keep-placeholder",
  closeBundle() {
    writeFileSync(`${outDir}/.gitkeep`, "");
  },
};

// The dev server proxies every API path to a docsearch-server on its default
// port, so `npm run dev` exercises the same auth and routes as the embedded
// build. DOCSEARCH_SERVER points it somewhere else.
const server = process.env.DOCSEARCH_SERVER ?? "http://127.0.0.1:8765";
const proxied = ["/docsearch.", "/mcp", "/upload", "/ui/config", "/.well-known"];

export default defineConfig({
  plugins: [svelte(), keepPlaceholder],
  build: {
    // docsearch-server embeds this directory; see internal/webui.
    outDir,
    emptyOutDir: true,
    assetsDir: "assets",
  },
  server: {
    proxy: Object.fromEntries(proxied.map((path) => [path, { target: server, changeOrigin: true }])),
  },
  test: {
    include: ["src/**/*.test.ts"],
    environment: "node",
  },
});

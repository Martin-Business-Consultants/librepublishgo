import path from "node:path";
import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";

// The admin builds into internal/admin/dist, which the Go binary embeds:
// `npm run build` here, then `go build`, and the admin ships inside the one
// executable. `npm run dev` proxies the API to a running librepublish.
export default defineConfig({
  base: "/",
  plugins: [svelte(), tailwindcss()],
  resolve: {
    alias: { $lib: path.resolve("./src/lib") },
  },
  build: {
    outDir: "../internal/admin/dist",
    emptyOutDir: true,
    assetsInlineLimit: 8192,
    chunkSizeWarningLimit: 600,
  },
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/files": "http://localhost:8080",
    },
  },
});

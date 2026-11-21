/// <reference types="vitest/config" />
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// R10.1 — the SPA is built here and embedded in the Go binary via go:embed
// (see web/embed.go). No separate web server, no CDN dependency: the only
// network call the running app makes is to its own origin.
export default defineConfig({
  // Absolute asset URLs. The app is served from the root of its own origin
  // (R10.1: the Go binary serves the SPA at `/`), and R10.16's routes are
  // paths — `/c/freeidp/freeidp-pg`. A relative `./assets/index.js` resolves
  // against the *current path* there, i.e. `/c/freeidp/assets/index.js`, so
  // every deep route would 404 its own bundle and render a blank page. `base:
  // "/"` is the only setting that is correct for a path-routed SPA.
  base: "/",
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // R11.4 reports the SPA's gzipped JS as a number of its own, and both the
    // Makefile and CI read this exact path — so the bundle is emitted
    // un-hashed. `make web` + `make build` stay reproducible without a
    // manifest lookup, and the committed web/dist is diffable in review.
    rolldownOptions: {
      output: {
        entryFileNames: "assets/index.js",
        chunkFileNames: "assets/[name].js",
        assetFileNames: "assets/[name][extname]",
      },
    },
    reportCompressedSize: true,
  },
  test: {
    environment: "jsdom",
    // Required for React Testing Library's automatic cleanup. RTL only
    // registers its afterEach(cleanup) hook when the Vitest globals exist;
    // without it every render in a file piles up in document.body and the
    // `screen.*` queries match the previous test's markup too.
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    // CSS is imported for side effects only; the visual layer is covered by
    // the Playwright screenshot suite (R16.7), not by jsdom.
    css: false,
  },
});

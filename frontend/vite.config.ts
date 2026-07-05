import { readFileSync } from "node:fs";
import { defineConfig } from "vite";

// Single source of truth for the displayed app version: the frontend package.json.
// Injected as a compile-time constant so the UI never hardcodes (and drifts from) it.
const pkg = JSON.parse(readFileSync(new URL("./package.json", import.meta.url), "utf-8")) as { version: string };

export default defineConfig({
  define: {
    __APP_VERSION__: JSON.stringify(pkg.version),
  },
  build: {
    emptyOutDir: false,
  },
});

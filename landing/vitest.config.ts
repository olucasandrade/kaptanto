import { defineConfig } from "vitest/config";
import tsconfigPaths from "vite-tsconfig-paths";

/**
 * Separate from vite.config.ts on purpose: the app's vite.config.ts loads the
 * qwikCity/qwikVite plugins, which expect to run inside the Qwik City routing
 * pipeline (dev server or build), not a plain Vitest environment. The smoke
 * suite only needs to import plain TS data modules and read source files, so
 * it gets its own minimal config instead of extending the app one.
 */
export default defineConfig({
  plugins: [tsconfigPaths({ root: "." })],
  test: {
    include: ["test/**/*.test.ts"],
    environment: "node",
  },
});

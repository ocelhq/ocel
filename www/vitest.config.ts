import path from "node:path";
import { fumadocsMdx } from "fumadocs-mdx/vite";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [fumadocsMdx({ index: false })],
  resolve: { alias: { "@": path.resolve(import.meta.dirname) } },
});

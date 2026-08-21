import { defineConfig } from "ocel/config";

export default defineConfig({
  slug: "hono",
  apps: [{ name: "hono", framework: "hono", path: "." }],
});

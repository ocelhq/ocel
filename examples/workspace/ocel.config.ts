import { defineConfig } from "ocel/config";

export default defineConfig({
  slug: "workspace",
  apps: [
    { name: "next", framework: "next", path: "../next" },
    { name: "express", framework: "express", path: "../express" },
    { name: "hono", framework: "hono", path: "../hono" },
  ],
});

import { defineConfig } from "ocel/config";

export default defineConfig({
  slug: "express",
  apps: [{ name: "web", framework: "express", path: "." }],
});

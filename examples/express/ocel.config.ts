import { defineConfig } from "ocel/config";

export default defineConfig({
  slug: "express",
  apps: [{ name: "express", framework: "express", path: "." }],
});

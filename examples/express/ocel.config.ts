import { defineConfig } from "ocel/config";

export default defineConfig({
  slug: "express",
  apps: [{ name: "exp", framework: "express", path: "." }],
});

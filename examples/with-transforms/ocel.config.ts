import { defineConfig } from "ocel/config";

export default defineConfig({
  slug: "with-transforms",
  apps: [{ name: "api", framework: "express", path: "." }],
});

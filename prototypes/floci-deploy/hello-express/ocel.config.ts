import { defineConfig } from "ocel/config";

export default defineConfig({
  slug: "hello-express",
  apps: [{ name: "web", framework: "express", path: "." }],
});

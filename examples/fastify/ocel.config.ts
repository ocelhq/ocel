import { defineConfig } from "ocel/config";

export default defineConfig({
  slug: "fastify",
  apps: [{ name: "fstfy", framework: "fastify", path: "." }],
});

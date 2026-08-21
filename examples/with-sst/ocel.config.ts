import { defineConfig } from "ocel/config";

export default defineConfig({
  slug: "with-sst",
  links: ["orders"],
  apps: [{ name: "api", framework: "express", path: "." }],
});

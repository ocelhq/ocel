import { defineConfig } from "ocel/config";

export default defineConfig({
  slug: "with-pulumi",
  links: ["orders"],
  apps: [{ name: "api", framework: "express", path: "." }],
});

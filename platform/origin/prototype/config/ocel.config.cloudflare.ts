import { defineConfig } from "./config.prototype";
import { cloudflareOrigin, neon } from "./vendors.prototype";

export default defineConfig({
  slug: "nxtest",
  origin: cloudflareOrigin(),
  resources: {
    postgres: neon({ project: "nxtest" }),
  },
});

import { defineConfig } from "./config.prototype";
import { aws, cloudflare, cloudflareDns, neon } from "./vendors.prototype";

export default defineConfig({
  slug: "nxtest",
  origin: aws({ region: "eu-west-1" }),
  edge: cloudflare(),
  dns: cloudflareDns(),
  resources: {
    postgres: neon(),
  },
});

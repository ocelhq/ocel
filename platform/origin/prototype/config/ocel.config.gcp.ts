import { defineConfig } from "./config.prototype";
import { cloudflare, cloudflareDns, gcp } from "./vendors.prototype";

export default defineConfig({
  slug: "nxtest",
  origin: gcp({ project: "nxtest-prod" }),
  edge: cloudflare(),
  dns: cloudflareDns(),
});

import { defineConfig } from "./config.prototype";
import { cloudflareDns, s3Keys, vps } from "./vendors.prototype";

export default defineConfig({
  slug: "nxtest",
  origin: vps({ host: "box.example.com" }),
  dns: cloudflareDns(),
  resources: {
    bucket: s3Keys({ endpoint: "https://s3.eu-west-1.amazonaws.com" }),
  },
});

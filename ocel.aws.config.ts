import awsProvider from "@ocel/provider-aws";
import { defineConfig } from "ocel/config";
import { cloudflareDns } from "ocel/dns";
import { cloudflare } from "ocel/edge";

export default defineConfig({
  slug: "ocelhq",
  edge: cloudflare(),
  dns: cloudflareDns(),
  provider: awsProvider(),
  apps: [
    { name: "www", runtime: "next", path: "./www" },
    { name: "github", runtime: "node", path: "./console/github" },
  ],
});

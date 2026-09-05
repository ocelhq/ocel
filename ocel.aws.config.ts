import { defineConfig } from "ocel/config";
import { cloudflare } from "ocel/edge";
import { cloudflareDns } from "ocel/dns";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "ocelhq",
  edge: cloudflare(),
  dns: cloudflareDns(),
  provider: awsProvider(),
  apps: [{ name: "www", runtime: "next", path: "./console/web" }],
});

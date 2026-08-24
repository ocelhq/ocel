import { defineConfig } from "ocel/config";
import { cloudflare } from "ocel/edge";
import awsProvider from "@ocel/provider-aws";
import { cloudflareDns } from "ocel/dns";

export default defineConfig({
  slug: "nxtest",
  provider: awsProvider(),
  edge: cloudflare(),
  dns: cloudflareDns(),
});

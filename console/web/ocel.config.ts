import awsProvider from "@ocel/provider-aws";
import { defineConfig } from "ocel/config";
import { cloudflare } from "ocel/edge";

export default defineConfig({
  slug: "ocel-web",
  provider: awsProvider(),
  edge: cloudflare(),
});

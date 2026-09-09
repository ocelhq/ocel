import { defineConfig } from "ocel/config";
import { cloudflare } from "ocel/edge";
import awsProvider from "ocel/providers/aws";

export default defineConfig({
  slug: "ocel-web",
  provider: awsProvider(),
  edge: cloudflare(),
});

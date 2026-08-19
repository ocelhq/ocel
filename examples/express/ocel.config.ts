import { defineConfig } from "ocel/config";
import { cloudflare } from "ocel/edge";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "express",
  provider: awsProvider(),
  edge: cloudflare(),
  apps: [{ name: "exp", framework: "express", path: "." }],
});

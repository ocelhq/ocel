import { defineConfig } from "ocel/config";
import { cloudflare } from "ocel/edge";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "proto630",
  provider: awsProvider(),
  edge: cloudflare(),
  domains: { preview: "*.proto630.example.com" },
  apps: [{ name: "api", framework: "express", path: "./examples/express" }],
});

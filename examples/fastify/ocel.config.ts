import { defineConfig } from "ocel/config";
import { cloudflare } from "ocel/edge";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "fastify",
  provider: awsProvider(),
  edge: cloudflare(),
  apps: [{ name: "fstfy", framework: "fastify", path: "." }],
});

import { defineConfig } from "@ocel/sdk/config";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "fastify",
  provider: awsProvider(),
  apps: [{ name: "fstfy", framework: "fastify", path: "." }],
});

import { defineConfig } from "ocel/config";
import { cfEdge } from "ocel/edge";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "fastify",
  provider: awsProvider(),
  edge: cfEdge(),
  apps: [{ name: "fstfy", framework: "fastify", path: "." }],
});

import { defineConfig } from "ocel/config";
import { cfEdge } from "ocel/edge";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "expr",
  provider: awsProvider(),
  edge: cfEdge(),
  apps: [{ name: "exp", framework: "express", path: "." }],
});

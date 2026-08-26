import { defineConfig } from "ocel/config";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "proto630",
  provider: awsProvider(),
  apps: [{ name: "web", framework: "next", path: "./examples/next" }],
});

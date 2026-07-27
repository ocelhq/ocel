import { defineConfig } from "@ocel/sdk/config";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "express",
  provider: awsProvider(),
  apps: [{ name: "exp", framework: "express", path: "." }],
});

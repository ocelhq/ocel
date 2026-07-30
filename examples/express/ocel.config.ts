import { defineConfig } from "ocel/config";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "express",
  provider: awsProvider(),
  apps: [{ name: "exp", framework: "express", path: "." }],
});

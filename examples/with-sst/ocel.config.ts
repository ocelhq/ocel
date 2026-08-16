import { defineConfig } from "ocel/config";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "with-sst",
  provider: awsProvider(),
  links: ["orders"],
  apps: [{ name: "api", framework: "express", path: "." }],
});

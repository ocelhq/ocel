import { defineConfig } from "ocel/config";
import { cloudflare } from "ocel/edge";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "with-transforms",
  provider: awsProvider({ transforms: ["./infra/defaults.transform.ts"] }),
  edge: cloudflare(),
  apps: [{ name: "api", framework: "express", path: "." }],
});

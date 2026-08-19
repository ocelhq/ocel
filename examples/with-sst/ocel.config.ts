import { defineConfig } from "ocel/config";
import { cloudflare } from "ocel/edge";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "with-sst",
  provider: awsProvider({ transforms: ["./infra/network.transform.ts"] }),
  edge: cloudflare(),
  links: ["orders"],
  apps: [{ name: "api", framework: "express", path: "." }],
});

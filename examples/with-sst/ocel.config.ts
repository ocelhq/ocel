import { defineConfig } from "ocel/config";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "with-sst",
  domains: { production: ["with-sst.floci.test"] },
  provider: awsProvider({ transforms: ["./infra/network.transform.ts"] }),
  links: ["orders"],
  apps: [{ name: "api", framework: "express", path: "." }],
});

import { defineConfig } from "ocel/config";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "with-pulumi",
  domains: { production: ["with-pulumi.floci.test"] },
  provider: awsProvider({ transforms: ["./infra/network.transform.ts"] }),
  links: ["orders"],
  apps: [{ name: "api", framework: "express", path: "." }],
});

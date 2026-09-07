import awsProvider from "@ocel/provider-aws";
import { defineConfig } from "ocel/config";

export default defineConfig({
  slug: "go",
  provider: awsProvider(),

  // The provider fronts the deployment with its own default edge. Name one instead:
  // edge: cloudfront(), // or apiGateway(), both from "@ocel/provider-aws/edge"
  // edge: cloudflare(), // from "ocel/edge"; the token and account id come from the environment

  apps: [
    {
      name: "web",
      path: "./server",
      runtime: "go",
      // The architecture the binary is built for, x86_64 unless named:
      // runtime: { name: "go", arch: "arm64" },
    },
  ],
});

import awsProvider from "@ocel/provider-aws";
import { defineConfig } from "ocel/config";

export default defineConfig({
  slug: "workspace",
  provider: awsProvider(),

  // The provider fronts the deployment with its own default edge. Name one instead:
  // edge: cloudfront(), // or apiGateway(), both from "@ocel/provider-aws/edge"
  // edge: cloudflare(), // from "ocel/edge"; the token and account id come from the environment

  // Hostname records go into the provider's own dns. Write them into cloudflare instead:
  // dns: cloudflareDns(), // from "ocel/dns"

  apps: [
    {
      name: "next",
      runtime: "next",
      path: "./apps/next",
      folder: "/next",
      // Serverless unless told otherwise; a container is one image serving every route:
      // compute: "container",
      // The hostname production serves on, bound with `ocel domain add`:
      // domains: { production: "next.example.com" },
    },
    {
      name: "express",
      path: "./apps/express",
      folder: "/express",
      // Serverless unless told otherwise; a container is one image serving every route:
      // compute: "container",
      // The hostname production serves on, bound with `ocel domain add`:
      // domains: { production: "express.example.com" },
    },
  ],
});

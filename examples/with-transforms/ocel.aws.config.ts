import awsProvider from "@ocel/provider-aws";
import { apiGateway, cloudfront } from "@ocel/provider-aws/edge";
import type { EdgeDescriptor } from "ocel/config";
import { defineConfig } from "ocel/config";
import { cloudflareDns } from "ocel/dns";
import { cloudflare } from "ocel/edge";
import base, { zonedApps } from "./ocel.config.ts";

const edges: Record<string, () => EdgeDescriptor> = {
  "api-gateway": apiGateway,
  cloudfront,
  cloudflare,
};

const edge = edges[process.env.OCEL_AWS_EDGE ?? ""];

export default defineConfig({
  ...base,
  provider: awsProvider({ transforms: ["./infra/defaults.transform.ts"] }),
  ...(edge ? { edge: edge() } : {}),
  ...(process.env.OCEL_JOURNEY_DNS === "cloudflare" ? { dns: cloudflareDns() } : {}),
  apps: zonedApps(),
});

import { variant } from "./types";

export const container = variant("container", {
  offeredOn: ["aws", "gcp"],
  config: { compute: "container" },
});

export const apiGateway = variant("api-gateway", {
  offeredOn: ["aws"],
  config: { edge: "api-gateway" },
});

export const cloudflare = variant("cloudflare", {
  offeredOn: ["aws"],
  config: { edge: "cloudflare" },
});

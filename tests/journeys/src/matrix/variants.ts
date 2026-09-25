import { JOURNEY_REGISTRY, REGISTRY_TOKEN_ENV } from "../registry/settings";
import { DEFAULT_VARIANT, TARGETS, type Variant, variant } from "./types";

export const defaults: Variant = { name: DEFAULT_VARIANT, offeredOn: TARGETS, config: {} };

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

export const registry = variant("registry", {
  offeredOn: ["vps"],
  config: { registry: { server: JOURNEY_REGISTRY, password: `\${${REGISTRY_TOKEN_ENV}}` } },
});

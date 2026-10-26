import { backendBucketChecks } from "../checks/backendBucket";
import { cloudfrontShieldChecks } from "../checks/cloudfrontShield";
import {
  CLIENT_CERTIFICATE_CHECKS,
  forwardedAppChecks,
  SHIELDED_ORIGIN_CHECKS,
} from "../checks/originShield";
import { imageShowsNoBindingCheck } from "../checks/prerender";
import { revisionTagChecks } from "../checks/revisionTag";
import { TUNNELED_ORIGIN_CHECKS } from "../checks/tunnel";
import { workersCacheChecks } from "../checks/workersCache";
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

export const cloudfrontShielded = variant("cloudfront-shield", {
  offeredOn: ["aws"],
  config: { edge: "cloudfront" },
  checks: cloudfrontShieldChecks,
});

export const cloudflare = variant("cloudflare", {
  offeredOn: ["aws"],
  config: { edge: "cloudflare" },
});

export const cloudflareInFrontOfContainers = variant("cloudflare-container", {
  offeredOn: ["aws"],
  config: { edge: "cloudflare", compute: "container" },
  checks: CLIENT_CERTIFICATE_CHECKS,
});

export const cloudflareInFrontOfMixedComputes = variant("cloudflare-mixed", {
  offeredOn: ["aws"],
  config: { edge: "cloudflare", computes: { express: "container" } },
  checks: forwardedAppChecks(["express"]),
});

export const cloudflareOnABox = variant("cloudflare", {
  offeredOn: ["vps"],
  config: { edge: "cloudflare" },
  checks: SHIELDED_ORIGIN_CHECKS,
});

export const cloudflareTunnel = variant("cloudflare-tunnel", {
  offeredOn: ["vps"],
  config: { edge: "cloudflare", tunnel: true },
  checks: TUNNELED_ORIGIN_CHECKS,
});

export const cloudflareOnGoogleCloud = variant("cloudflare", {
  offeredOn: ["gcp"],
  config: { edge: "cloudflare" },
  checks: revisionTagChecks,
});

const CACHING_CONTAINERS = {
  edge: "cloudflare",
  compute: "container",
  cache: true,
} as const;

export const cloudflareCachingContainers = variant("cloudflare-container-cache", {
  offeredOn: ["gcp"],
  config: CACHING_CONTAINERS,
  checks: workersCacheChecks,
});

export const cloudflareCachingContainersShielded = variant("cloudflare-container-cache", {
  offeredOn: ["aws"],
  config: CACHING_CONTAINERS,
  checks: [...CLIENT_CERTIFICATE_CHECKS, ...workersCacheChecks],
});

export const cloudflareCachingNextContainer = variant("cloudflare-container-cache", {
  offeredOn: ["aws", "gcp"],
  config: CACHING_CONTAINERS,
});

export const cloudflareCachingABox = variant("cloudflare-cache", {
  offeredOn: ["vps"],
  config: { edge: "cloudflare", cache: true },
  checks: [...SHIELDED_ORIGIN_CHECKS, ...workersCacheChecks],
});

export const cloudflareCachingNextOnABox = variant("cloudflare-cache", {
  offeredOn: ["vps"],
  config: { edge: "cloudflare", cache: true },
  checks: SHIELDED_ORIGIN_CHECKS,
});

export const cloudflareTunnelCaching = variant("cloudflare-tunnel-cache", {
  offeredOn: ["vps"],
  config: { edge: "cloudflare", tunnel: true, cache: true },
  checks: [...TUNNELED_ORIGIN_CHECKS, ...workersCacheChecks],
});

export const alb = variant("alb", { offeredOn: ["gcp"], config: { edge: "alb" } });

export const albInFrontOfNext = { ...alb, checks: backendBucketChecks };

const NEXT_START = { dockerfile: "next-start.Dockerfile" };

export const containerDockerfile = variant("container-dockerfile", {
  offeredOn: ["aws", "gcp"],
  config: { compute: "container", image: NEXT_START },
});

export const albInFrontOfNextContainer = variant("alb-container", {
  offeredOn: ["gcp"],
  config: { edge: "alb", compute: "container" },
  checks: backendBucketChecks,
});

export const albContainerDockerfile = variant("alb-container-dockerfile", {
  offeredOn: ["gcp"],
  config: { edge: "alb", compute: "container", image: NEXT_START },
});

export const cloudflareCachingContainerDockerfile = variant("cloudflare-dockerfile-cache", {
  offeredOn: ["aws", "gcp"],
  config: { ...CACHING_CONTAINERS, image: NEXT_START },
});

export const registry = variant("registry", {
  offeredOn: ["vps"],
  config: { registry: { server: JOURNEY_REGISTRY, password: `\${${REGISTRY_TOKEN_ENV}}` } },
});

const PRERENDER_DOCKERFILE = { dockerfile: "prerender.Dockerfile" };

export const dockerfile = variant("dockerfile", {
  offeredOn: ["vps"],
  config: { image: PRERENDER_DOCKERFILE },
  checks: [imageShowsNoBindingCheck],
});

export const cloudflareCachingDockerfile = variant("cloudflare-dockerfile-cache", {
  offeredOn: ["vps"],
  config: { edge: "cloudflare", cache: true, image: PRERENDER_DOCKERFILE },
  checks: [...SHIELDED_ORIGIN_CHECKS, imageShowsNoBindingCheck],
});

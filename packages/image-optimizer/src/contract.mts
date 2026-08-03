// The wire contract between the edge and this function, and the shapes the
// build hands it. Both sides are already shipped: ImageOriginRequest is
// workers/nextjs/src/image.ts's own interface and CompiledImageConfig is
// packages/next-runtime/src/image-config.mts's. They are restated here rather
// than imported because this artifact is account-global — it is built once, at
// its own version, and serves every app in the substrate, so it may not take a
// build-time dependency on either the worker or the adapter.
//
// Any drift between these declarations and theirs is a bug in this file.

// What the edge asks for. It is the whole of what the worker may say about a
// request: the worker holds zero authority, so every field here is re-derived
// or re-checked below before a byte is fetched.
//
// mimeType is the type the edge negotiated out of `accept`. It is sent rather
// than re-derived because the colo cache key commits to it: were this function
// to negotiate its own and land anywhere else, the entry would be addressed as
// one format and hold another.
export interface ImageOriginRequest {
  slug: string;
  app: string;
  buildId: string;
  url: string;
  w: number;
  q: number;
  accept: string;
  mimeType: string;
  configHash: string;
}

export interface CompiledRemotePattern {
  protocol?: string;
  hostname: string;
  port?: string;
  pathname: string;
  search?: string;
}

export interface CompiledLocalPattern {
  pathname: string;
  search?: string;
}

// The artifact at image-config/<slug>/<app>/<buildId>.json. Every glob arrived
// already compiled to a regex source, so neither this function nor the edge
// carries a glob library and neither can disagree with the picomatch the build
// matched with.
//
// Absent keys carry meaning: no `localPatterns` means every local path is
// allowed, no `qualities` means any quality in 1..100. An empty array means the
// opposite of absence in both cases.
export interface CompiledImageConfig {
  path: string;
  deviceSizes: number[];
  imageSizes: number[];
  qualities?: number[];
  formats: string[];
  domains: string[];
  minimumCacheTTL: number;
  maximumRedirects: number;
  maximumResponseBody: number;
  dangerouslyAllowSVG: boolean;
  dangerouslyAllowLocalIP: boolean;
  contentSecurityPolicy: string;
  contentDispositionType: string;
  remotePatterns: CompiledRemotePattern[];
  localPatterns?: CompiledLocalPattern[];
}

// What this function answers with. Modelled as data rather than as a Response
// so the whole pipeline is testable without a runtime that has one, and so the
// Lambda adapter is the only place that knows about streaming.
export interface OriginResponse {
  status: number;
  headers: Record<string, string>;
  body: Uint8Array;
}

// The diagnostic header the edge reads to force ttl = minimumCacheTTL and then
// strips. Must stay identical to IMAGE_PASSTHROUGH in
// workers/nextjs/src/image.ts — the edge looks for this exact name, and a
// rename on either side silently gives a failed transform the upstream's TTL.
export const IMAGE_PASSTHROUGH = "x-ocel-image-passthrough";

// The /_next/image edge route: everything the worker can decide without
// touching an image byte. Validation reproduces Next's own
// ImageOptimizerCache.validateParams — same checks, same order, same message
// text — against the patterns the build precompiled into the routing manifest,
// so the edge and `next dev` agree by construction rather than by resemblance.
//
// The origin holds all the authority (it reloads the config from S3 and
// re-validates against it); this tier exists to turn a bad request into a 400
// without spending an invocation on it.

import { mediaType } from "./accept";

// The `images` section of the routing manifest: PR 1's compiled output plus the
// hash of the artifact the optimizer will independently load and verify. Every
// glob arrives as a regex source, because the worker carries no glob library.
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

export interface ImageConfig {
  path: string;
  deviceSizes: number[];
  imageSizes: number[];
  // Absent means "any quality in 1..100". An empty array means none.
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
  // Absent means "every local path is allowed". An empty array denies all.
  localPatterns?: CompiledLocalPattern[];
  configHash: string;
}

// What the origin is asked to produce. The worker holds no authority, so this
// is the whole of what it may say about a request: the identity of the build,
// the already-validated parameters, and the hash of the config it validated
// against — which the origin re-derives from the artifact it loads itself and
// refuses to serve without matching.
//
// slug and app are that identity's other two thirds: the origin loads the
// config from image-config/<slug>/<app>/<buildId>.json, so no two of the three
// may be dropped here and reconstructed there.
export interface ImageOriginRequest {
  slug: string;
  app: string;
  buildId: string;
  url: string;
  w: number;
  q: number;
  accept: string;
  configHash: string;
}

export type ImageOrigin = (payload: ImageOriginRequest) => Promise<Response>;

export interface ImageDeps {
  config: ImageConfig;
  basePath: string;
  slug: string;
  app: string;
  buildId: string;
  origin: ImageOrigin;
}

const DUMMY_ORIGIN = "http://n";

// A url that resolves to /_next/image is a request for this route to call
// itself. Matched against the *decoded* pathname and anywhere in it, not as a
// prefix: an assetPrefix in front of it, or a percent-encoded letter inside it,
// defeats a prefix check (CVE-2024-47831).
const RECURSIVE = /\/_next\/image($|\/)/;

const INTEGER = /^[0-9]+$/;

export type ImageValidation =
  | { ok: true; params: ImageParams }
  | { ok: false; status: number; message: string };

export interface ImageParams {
  href: string;
  width: number;
  quality: number;
  // '' whenever Accept produced no negotiated format — see getSupportedMimeType.
  mimeType: string;
  isAbsolute: boolean;
  isStatic: boolean;
}

function invalid(message: string): ImageValidation {
  return { ok: false, status: 400, message };
}

// A url Next itself cannot parse or decode: it throws, and its server answers
// 500. Reproduced as a status rather than as a throw, because an uncaught
// exception here is an unauthenticated Worker crash page.
function malformed(): ImageValidation {
  return { ok: false, status: 500, message: "Internal Server Error" };
}

function parseUrl(url: string): URL | undefined {
  try {
    return new URL(url, DUMMY_ORIGIN);
  } catch {
    return undefined;
  }
}

function decode(value: string): string | undefined {
  try {
    return decodeURIComponent(value);
  } catch {
    return undefined;
  }
}

// The compiled patterns are per-config and per-isolate, so their regexes are
// built once and reused rather than recompiled on every request.
const REGEXES = new Map<string, RegExp>();

function regex(source: string): RegExp {
  let compiled = REGEXES.get(source);
  if (!compiled) REGEXES.set(source, (compiled = new RegExp(source)));
  return compiled;
}

function matchLocalPattern(pattern: CompiledLocalPattern, url: URL): boolean {
  if (pattern.search !== undefined && pattern.search !== url.search) {
    return false;
  }
  return regex(pattern.pathname).test(url.pathname);
}

// Matched against url.hostname, never url.host: the compiled hostname regex is
// anchored and knows nothing of ports or userinfo, so `**.example.com` against
// a `host` of "cdn.example.com:8443" would fail a pattern Next matches, and
// `hostname` is also what strips the userinfo an attacker would otherwise use
// to prefix an allowlisted name onto a host they control.
function matchRemotePattern(pattern: CompiledRemotePattern, url: URL): boolean {
  // The pattern side arrives with its trailing colon already stripped, so the
  // url side is stripped here rather than the pattern being reconstructed.
  if (
    pattern.protocol !== undefined &&
    pattern.protocol !== url.protocol.replace(/:$/, "")
  ) {
    return false;
  }
  if (pattern.port !== undefined && pattern.port !== url.port) return false;
  if (!regex(pattern.hostname).test(url.hostname)) return false;
  if (pattern.search !== undefined && pattern.search !== url.search) {
    return false;
  }
  return regex(pattern.pathname).test(url.pathname);
}

function hasRemoteMatch(
  domains: string[],
  patterns: CompiledRemotePattern[],
  url: URL,
): boolean {
  return (
    domains.some((domain) => url.hostname === domain) ||
    patterns.some((pattern) => matchRemotePattern(pattern, url))
  );
}

// A repeated key is an array to Next's query parser and a distinct row in the
// validation table, which URLSearchParams.get() would erase.
function param(url: URL, name: string): string | string[] | undefined {
  const values = url.searchParams.getAll(name);
  if (values.length === 0) return undefined;
  return values.length === 1 ? values[0] : values;
}

export function validateImageRequest(
  url: URL,
  accept: string,
  config: ImageConfig,
  basePath: string,
): ImageValidation {
  const rawUrl = param(url, "url");
  const w = param(url, "w");
  const q = param(url, "q");

  if (!rawUrl) return invalid('"url" parameter is required');
  if (Array.isArray(rawUrl)) return invalid('"url" parameter cannot be an array');
  if (rawUrl.length > 3072) return invalid('"url" parameter is too long');
  // Before the relative/absolute branch, always: //evil.example is a host the
  // absolute branch would have checked and the relative branch would not.
  if (rawUrl.startsWith("//")) {
    return invalid('"url" parameter cannot be a protocol-relative URL (//)');
  }

  let href: string;
  let isAbsolute: boolean;
  if (rawUrl.startsWith("/")) {
    href = rawUrl;
    isAbsolute = false;
    const parsed = parseUrl(rawUrl);
    const pathname = decode(parsed?.pathname ?? "");
    if (pathname === undefined) return malformed();
    if (RECURSIVE.test(pathname)) {
      return invalid('"url" parameter cannot be recursive');
    }
    // Absent patterns allow everything and are answered before the url is
    // parsed — which is also the only reason an unparseable one can reach here.
    if (config.localPatterns) {
      if (!parsed) return malformed();
      const allowed = config.localPatterns.some((pattern) =>
        matchLocalPattern(pattern, parsed),
      );
      if (!allowed) return invalid('"url" parameter is not allowed');
    }
  } else {
    const parsed = parseAbsolute(rawUrl);
    if (!parsed) return invalid('"url" parameter is invalid');
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return invalid('"url" parameter is invalid');
    }
    if (!hasRemoteMatch(config.domains, config.remotePatterns, parsed)) {
      return invalid('"url" parameter is not allowed');
    }
    href = parsed.toString();
    isAbsolute = true;
  }

  if (!w) return invalid('"w" parameter (width) is required');
  if (Array.isArray(w)) return invalid('"w" parameter (width) cannot be an array');
  // Before parseInt, always: parseInt("99.9") is 99, so the regex is the only
  // thing standing between a fractional width and a cache entry keyed on 99.
  if (!INTEGER.test(w)) {
    return invalid('"w" parameter (width) must be an integer greater than 0');
  }

  if (!q) return invalid('"q" parameter (quality) is required');
  if (Array.isArray(q)) return invalid('"q" parameter (quality) cannot be an array');
  if (!INTEGER.test(q)) {
    return invalid('"q" parameter (quality) must be an integer between 1 and 100');
  }

  const width = parseInt(w, 10);
  if (width <= 0 || Number.isNaN(width)) {
    return invalid('"w" parameter (width) must be an integer greater than 0');
  }
  if (![...config.deviceSizes, ...config.imageSizes].includes(width)) {
    return invalid(`"w" parameter (width) of ${width} is not allowed`);
  }

  const quality = parseInt(q, 10);
  if (Number.isNaN(quality) || quality < 1 || quality > 100) {
    return invalid('"q" parameter (quality) must be an integer between 1 and 100');
  }
  if (config.qualities && !config.qualities.includes(quality)) {
    return invalid(`"q" parameter (quality) of ${q} is not allowed`);
  }

  return {
    ok: true,
    params: {
      href,
      width,
      quality,
      mimeType: getSupportedMimeType(config.formats, accept),
      isAbsolute,
      isStatic: rawUrl.startsWith(`${basePath}/_next/static/media`),
    },
  };
}

function parseAbsolute(url: string): URL | undefined {
  try {
    return new URL(url);
  } catch {
    return undefined;
  }
}

// Next's own guard, verbatim in effect: resolve the best media type from Accept
// against the configured formats, then keep it only if the header literally
// contains it. A wildcard-only Accept ("*/*", "image/*", or none at all)
// therefore negotiates nothing and the output format falls back to the source.
// This is undocumented upstream and is the single behavior a reimplementation
// gets wrong; test/image-conformance.test.ts asserts it against real Next
// output.
export function getSupportedMimeType(formats: string[], accept = ""): string {
  const mimeType = mediaType(accept, formats);
  return accept.includes(mimeType) ? mimeType : "";
}

// Next strips basePath and then tests the remainder against /_next/image, so an
// app under a basePath serves the route at both /docs/_next/image and a bare
// /_next/image. Matching is by prefix (handleNextImageRequest), which makes
// /_next/imageBAD an image request that fails validation rather than a static
// asset that 404s.
export function isImageRequest(pathname: string, basePath: string): boolean {
  return withoutBasePath(pathname, basePath).startsWith("/_next/image");
}

function withoutBasePath(pathname: string, basePath: string): string {
  if (!basePath || !pathname.startsWith(basePath)) return pathname;
  const rest = pathname.slice(basePath.length);
  return rest.startsWith("/") ? rest : pathname;
}

// Bare, with no Content-Type — which is what Next sends, and what the
// conformance fixtures record. Encoded rather than passed as a string because
// a string body makes the runtime stamp one on for us.
function errorResponse(status: number, message: string): Response {
  return new Response(new TextEncoder().encode(message), { status });
}

export async function serveImage(request: Request, url: URL, deps: ImageDeps): Promise<Response> {
  const accept = request.headers.get("accept") ?? "";
  const result = validateImageRequest(url, accept, deps.config, deps.basePath);
  if (!result.ok) return errorResponse(result.status, result.message);

  return deps.origin({
    slug: deps.slug,
    app: deps.app,
    buildId: deps.buildId,
    url: result.params.href,
    w: result.params.width,
    q: result.params.quality,
    accept,
    configHash: deps.config.configHash,
  });
}

// The seam the optimizer lands behind. A validated request with nowhere to go
// is a 502 and is never cached: the request was well-formed, and it is the
// substrate — not the client — that could not answer it. PR 4 wraps this in the
// colo cache and PR 5 replaces the body with the signed Function URL call;
// ImageOrigin is what every caller is written against, so neither moves them.
export const unprovisionedImageOrigin: ImageOrigin = async () =>
  new Response("No image optimizer is provisioned for this deployment.", {
    status: 502,
    headers: { "content-type": "text/plain; charset=utf-8" },
  });

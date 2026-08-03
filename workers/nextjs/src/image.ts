// The /_next/image edge route: everything the worker can decide without
// touching an image byte. Validation reproduces Next's own
// ImageOptimizerCache.validateParams — same checks, same order, same message
// text — against the patterns the build precompiled into the routing manifest,
// so the edge and `next dev` agree by construction rather than by resemblance.
//
// The origin holds all the authority (it reloads the config from S3 and
// re-validates against it); this tier exists to turn a bad request into a 400
// without spending an invocation on it, and a repeat request into a colo hit
// without spending one either — which is why the cache key and the TTL
// derivation live here too, next to the validation that names their inputs.

import { mediaType } from "./accept";
import {
  answerableImageRequest,
  deltaSeconds,
  serveCachedImage,
  withStatus,
  NEXT_CACHE_STATUS,
  type CacheDeps,
} from "./cache";
import {
  durableImageOrigin,
  durableImageRefresh,
  imageObjectKey,
  type ImageStore,
} from "./image-store";

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
//
// accept is the raw header and mimeType is what this tier negotiated out of it.
// Both are sent because the cache key commits to the negotiated type: were the
// origin to negotiate its own from the raw header and land anywhere else, the
// entry would be addressed as one format and hold another. mimeType is the
// answer; accept remains for everything else a transform may want from it.
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

export type ImageOrigin = (payload: ImageOriginRequest) => Promise<Response>;

export interface ImageDeps {
  config: ImageConfig;
  basePath: string;
  slug: string;
  app: string;
  buildId: string;
  origin: ImageOrigin;
  // The build's content hash per served asset path (PR 1). It is what makes an
  // optimized image outlive the build it was first requested under: identical
  // bytes keep their key across a redeploy, changed bytes cannot answer under
  // the old one. Absent — an older manifest, or a path the build never hashed —
  // falls back to a build-scoped identity, which is correct but flushes on
  // every deploy.
  assetHashes?: Record<string, string>;
  // Absent outside a Worker request (and in routing tests): the image is served
  // uncached rather than not at all.
  cache?: CacheDeps;
  // The durable tier under the colo cache (see image-store.ts), engaged only
  // alongside `cache` — the write it does is fire-and-forget and needs that
  // waitUntil. Absent on a substrate that bound no cache store, which leaves
  // the read path exactly the colo cache and the optimizer.
  imageStore?: ImageStore;
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
      isStatic: STATIC_IMPORT_PREFIXES.some((prefix) =>
        rawUrl.startsWith(`${basePath}${prefix}`),
      ),
    },
  };
}

// Where the build writes the images an `import logo from "./logo.png"` emits.
// Their content hash is in the filename, so the bytes behind one of these paths
// can never change and the response is immutable rather than revalidated.
const STATIC_IMPORT_PREFIXES = [
  "/_next/static/media",
  "/_next/static/immutable/media",
];

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
  // A rejection is bare except for the cache status — no Vary, no Content-Type,
  // no Cache-Control, because Next's own 400s carry none of those and the
  // fixtures record it. x-ocel-cache is not Next's to match: it is the tier
  // signal an operator reads, and a route that stamped it on a 502 but not on a
  // 400 would be answering the same question two ways.
  if (!result.ok) {
    return withStatus(errorResponse(result.status, result.message), "BYPASS");
  }

  const { params } = result;
  const origin = async () =>
    servedImage(
      await deps.origin({
        slug: deps.slug,
        app: deps.app,
        buildId: deps.buildId,
        url: params.href,
        w: params.width,
        q: params.quality,
        accept,
        mimeType: params.mimeType,
        configHash: deps.config.configHash,
      }),
      deps.config,
    );

  const { digest, key } = await imageCacheKey(params, deps);

  // The durable tier engages only where the colo tier above it does: it needs
  // that tier's waitUntil, and a method the colo tier may not answer from is a
  // method this one may not read or write for either.
  let readThrough = origin;
  let refresh = origin;
  if (deps.imageStore && deps.cache && answerableImageRequest(request)) {
    const objectKey = imageObjectKey(deps.slug, digest);
    readThrough = durableImageOrigin(deps.imageStore, deps.cache, objectKey, origin);
    // A local source's key is a content hash of the file, so the stored bytes
    // are the only bytes it can ever name; a remote source's key is a url, whose
    // content the worker cannot know — so only that one has anything to re-derive
    // when the colo entry goes stale.
    refresh = params.isAbsolute
      ? durableImageRefresh(deps.imageStore, deps.cache, objectKey, origin)
      : readThrough;
  }

  return serveCachedImage(
    request,
    { key },
    deps.cache,
    readThrough,
    refresh,
    // Immutability is a property of the url this request used, not of the bytes
    // behind it: a static import and its public/ twin are the same file, hash to
    // the same key and share one entry, and only the hashed url may promise a
    // browser it will never change.
    params.isStatic ? IMMUTABLE : undefined,
  );
}

// The optimizer's own header, marking a response it could not transform and
// passed through unmodified. It is the one signal that separates a served
// original from an optimized image, and the TTL rule turns on it.
export const IMAGE_PASSTHROUGH = "x-ocel-image-passthrough";

const IMMUTABLE = "public, max-age=315360000, immutable";

// The optimizer relays the upstream image server's Cache-Control as its own —
// the worker never talks to that server, so this is the only place that
// directive exists at the edge. It is read for the TTL and then replaced: what
// some third-party CDN told our origin is not what this deployment tells a
// browser.
//
// A passthrough is the one case where it is not read at all. The bytes are the
// ones the transform failed on, and Next holds those for the configured minimum
// and no longer, whatever the upstream would have allowed.
//
// What comes out is the entry's window and the browser's default alike. The one
// request-scoped claim on top of it — immutable, for a content-hashed url — is
// serveCachedImage's, because it is not true of the bytes and so cannot be
// stored with them.
function servedImage(response: Response, config: ImageConfig): Response {
  if (response.status !== 200) return response;

  const passthrough = response.headers.has(IMAGE_PASSTHROUGH);
  const upstream = passthrough
    ? 0
    : (deltaSeconds(response.headers.get("cache-control"), "s-maxage", "max-age") ?? 0);
  const ttl = Math.max(config.minimumCacheTTL, upstream);

  const headers = new Headers(response.headers);
  // Read above and dropped here, before the entry is written: that the transform
  // failed is the deployment's business, not the browser's.
  headers.delete(IMAGE_PASSTHROUGH);
  headers.set("vary", "accept");
  headers.set("cache-control", `public, max-age=${ttl}, must-revalidate`);
  // The freshness of the response as it leaves the optimizer. Every tier above
  // restates it from the entry it answers with; nothing else ever writes MISS.
  headers.set(NEXT_CACHE_STATUS, "MISS");

  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}

// Bumping this invalidates every optimized image in every tier at once, without
// touching a stored byte: nothing can be addressed under the old key again.
const KEY_VERSION = 1;

// The identity of one optimized variant. configHash is not optional decoration:
// without it, tightening remotePatterns would leave every variant admitted
// under the looser config still addressable. The negotiated mimeType stands in
// for the whole Accept header — the output bytes depend on Accept only through
// that negotiation, and the formats it negotiates against are themselves
// covered by configHash — so a hundred browsers' Accept strings collapse onto
// the single entry they all describe.
//
// Both forms of the one identity: the digest itself, which is how the durable
// tier names its object, and the synthetic url the colo cache is keyed by.
async function imageCacheKey(
  params: ImageParams,
  deps: ImageDeps,
): Promise<{ digest: string; key: string }> {
  const digest = await sha256(
    JSON.stringify([
      KEY_VERSION,
      deps.slug,
      sourceIdentity(params, deps),
      params.width,
      params.quality,
      params.mimeType,
      deps.config.configHash,
    ]),
  );
  return { digest, key: `https://image.ocel/${digest}` };
}

// What the optimized bytes were made from. A remote source is named by its
// normalized absolute url and nothing else — the worker cannot know its
// content. A local one is named by the content hash the build took of it, so
// the variant survives a redeploy that did not touch the file and is
// unreachable by one that did.
function sourceIdentity(params: ImageParams, deps: ImageDeps): string {
  if (params.isAbsolute) return params.href;
  const path = assetPath(params.href, deps.basePath);
  const hash = path === undefined ? undefined : deps.assetHashes?.[path];
  return hash ?? `${deps.buildId}${normalized(params.href)}`;
}

// The href as the build's asset hash map keys it: the pathname alone (a query
// string names no file), decoded, with the basePath the browser saw stripped
// back off — the map is written from the build's output paths, which know
// nothing of where the app is mounted.
//
// Under a basePath, a path that does not carry it names no file this deployment
// serves, so it gets no hash rather than the hash of the file it resembles.
function assetPath(href: string, basePath: string): string | undefined {
  const parsed = parseUrl(href);
  if (!parsed) return undefined;
  const pathname = decode(parsed.pathname);
  if (pathname === undefined) return undefined;
  if (basePath && !pathname.startsWith(`${basePath}/`)) return undefined;
  return pathname.slice(basePath.length);
}

// The href as the fallback identity names it, so that the two branches agree on
// what one source is. Next's default localPatterns admit /x/../a.png alongside
// /a.png; new URL resolves both to the same pathname, which is what the hash-map
// branch has always keyed on. The query survives — a local route may serve
// different bytes per query, and unlike a path it names no file to hash.
function normalized(href: string): string {
  const parsed = parseUrl(href);
  const pathname = parsed && decode(parsed.pathname);
  return pathname ? `${pathname}${parsed.search}` : href;
}

async function sha256(value: string): Promise<string> {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(value),
  );
  return [...new Uint8Array(digest)]
    .map((byte) => byte.toString(16).padStart(2, "0"))
    .join("");
}

// The seam the optimizer lands behind. A validated request with nowhere to go
// is a 502 and is never cached: the request was well-formed, and it is the
// substrate — not the client — that could not answer it. This is what a
// substrate with no provisioned optimizer keeps answering, and what a substrate
// that has one falls back to when the call itself cannot be made.
export const unprovisionedImageOrigin: ImageOrigin = async () =>
  new Response("No image optimizer is provisioned for this deployment.", {
    status: 502,
    headers: { "content-type": "text/plain; charset=utf-8" },
  });

// functionUrlImageOrigin POSTs the validated request to the substrate's image
// optimizer, or is undefined when the substrate named none — the caller then
// keeps unprovisionedImageOrigin and every valid image request stays a 502,
// exactly as it did before an optimizer existed.
//
// doFetch is the worker's SigV4-signing origin fetch: the optimizer's Function
// URL is AWS_IAM-gated like every app Lambda, and this call is signed by the same
// edge credentials through the same path. The whole request is the JSON body —
// the optimizer reads no header for any purpose — so nothing of the client's
// request reaches it but the fields the edge already validated.
//
// A URL that is not a URL binds nothing rather than throwing per request, and a
// call that cannot complete is the substrate's 502 rather than an uncaught
// exception: this route runs ahead of middleware on an unauthenticated path, and
// a throw here is a Worker crash page.
export function functionUrlImageOrigin(
  url: string | undefined,
  doFetch: typeof fetch,
): ImageOrigin | undefined {
  if (!url || !parseAbsolute(url)) return undefined;
  return async (payload) => {
    try {
      return relayed(
        await doFetch(url, {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify(payload),
        }),
      );
    } catch {
      return unprovisionedImageOrigin(payload);
    }
  };
}

// The error statuses the optimizer answers with for itself, and the only ones
// whose body may reach a client. 400 is its re-validation, 500 its transform
// failure, 502 its upstream's; all three are Next's own messages, pinned by the
// conformance fixtures.
const OPTIMIZER_STATUSES = new Set([400, 500, 502]);

// Everything else on this hop was written by AWS, not by the optimizer, and this
// route is unauthenticated. A Function URL that refuses the call answers 403 with
// AWS's own IAM denial — "User: arn:aws:iam::<account>:user/ocel-edge is not
// authorized to perform: lambda:InvokeFunctionUrl on resource: arn:aws:lambda:…"
// — which names the account, the region, the edge identity and the function.
// 403 is a live state, not a hypothetical: missing or rotated edge credentials
// 403 every route. So an unrecognised status becomes the substrate's own 502 and
// the body is discarded unread. 502 is also the status the colo tier refuses to
// store, so nothing AWS wrote is cached either.
//
// A 200, though, is not enough on its own. The Function URL is RESPONSE_STREAM,
// which commits its status line before the handler is entered, so a function
// that never initialises — a bundle that throws while its module graph loads, a
// missing native binary — still answers 200, with Lambda's own JSON error
// payload for a body: errorType, errorMessage, and a stack trace naming
// /var/task and every bundled dependency's version. That is what a released
// artifact did, and what a browser was served as an image.
//
// The optimizer types every 200 it writes, so the type is what separates its
// answer from the runtime's. An untyped or non-image 200 is the substrate
// failing, which is the 502 below: the body is discarded unread, and 502 is the
// status neither tier will store, so a crashing optimizer poisons no cache.
function relayed(response: Response): Response {
  const own =
    response.status === 200
      ? isImage(response.headers.get("content-type"))
      : OPTIMIZER_STATUSES.has(response.status);
  if (own) return response;
  response.body?.cancel().catch(() => {});
  return new Response("The image optimizer could not be reached.", {
    status: 502,
    headers: { "content-type": "text/plain; charset=utf-8" },
  });
}

// The type as it was written. Casing and parameters belong to whoever produced
// the bytes; this only asks which family they claimed.
function isImage(contentType: string | null): boolean {
  return contentType?.trimStart().toLowerCase().startsWith("image/") ?? false;
}

import type {
  CompiledLocalPattern,
  CompiledRemotePattern,
  ImageConfig,
} from "@framework/next-protocol/routing-manifest";

import { mediaType } from "./accept.mjs";
import {
  deltaSeconds,
  headResponse,
  withStatus,
  NEXT_CACHE_STATUS,
} from "./http-cache.mjs";

export type { CompiledLocalPattern, CompiledRemotePattern, ImageConfig };

export interface ImageOriginRequest {
  assetPrefix: string;
  url: string;
  w: number;
  q: number;
  accept: string;
  mimeType: string;
  configHash: string;
}

export type ImageOrigin = (payload: ImageOriginRequest) => Promise<Response>;

export interface ImageCacheContext {
  request: Request;
  key: string;
  digest: string;
  absolute: boolean;
  servedCacheControl?: string;
  origin: () => Promise<Response>;
}

export type ImageCache = (context: ImageCacheContext) => Promise<Response>;

export interface ImageDeps {
  config: ImageConfig;
  basePath: string;
  assetPrefix: string;
  slug: string;
  app: string;
  deploymentId: string;
  origin: ImageOrigin;
  assetHashes?: Record<string, string>;
  imageCache?: ImageCache;
}

const DUMMY_ORIGIN = "http://n";

const RECURSIVE = /\/_next\/image($|\/)/;

const INTEGER = /^[0-9]+$/;

export type ImageValidation =
  | { ok: true; params: ImageParams }
  | { ok: false; status: number; message: string };

export interface ImageParams {
  href: string;
  width: number;
  quality: number;
  mimeType: string;
  isAbsolute: boolean;
  isStatic: boolean;
}

function invalid(message: string): ImageValidation {
  return { ok: false, status: 400, message };
}

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

function matchRemotePattern(pattern: CompiledRemotePattern, url: URL): boolean {
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

export function getSupportedMimeType(formats: string[], accept = ""): string {
  const mimeType = mediaType(accept, formats);
  return accept.includes(mimeType) ? mimeType : "";
}

export function isImageRequest(pathname: string, basePath: string): boolean {
  return withoutBasePath(pathname, basePath).startsWith("/_next/image");
}

function withoutBasePath(pathname: string, basePath: string): string {
  if (!basePath || !pathname.startsWith(basePath)) return pathname;
  const rest = pathname.slice(basePath.length);
  return rest.startsWith("/") ? rest : pathname;
}

function errorResponse(status: number, message: string): Response {
  return new Response(new TextEncoder().encode(message), { status });
}

export async function serveImage(request: Request, url: URL, deps: ImageDeps): Promise<Response> {
  const accept = request.headers.get("accept") ?? "";
  const result = validateImageRequest(url, accept, deps.config, deps.basePath);
  if (!result.ok) {
    return withStatus(errorResponse(result.status, result.message), "BYPASS");
  }

  const { params } = result;
  const origin = async () =>
    servedImage(
      await deps.origin({
        assetPrefix: deps.assetPrefix,
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
  const servedCacheControl = params.isStatic ? IMMUTABLE : undefined;

  if (deps.imageCache) {
    return deps.imageCache({
      request,
      key,
      digest,
      absolute: params.isAbsolute,
      servedCacheControl,
      origin,
    });
  }

  const served = withStatus(await origin(), "BYPASS");
  if (servedCacheControl && served.status === 200) {
    served.headers.set("cache-control", servedCacheControl);
  }
  return request.method === "HEAD" ? headResponse(served) : served;
}

export const IMAGE_PASSTHROUGH = "x-ocel-image-passthrough";

const IMMUTABLE = "public, max-age=315360000, immutable";

function servedImage(response: Response, config: ImageConfig): Response {
  if (response.status !== 200) return response;

  const passthrough = response.headers.has(IMAGE_PASSTHROUGH);
  const upstream = passthrough
    ? 0
    : (deltaSeconds(response.headers.get("cache-control"), "s-maxage", "max-age") ?? 0);
  const ttl = Math.max(config.minimumCacheTTL, upstream);

  const headers = new Headers(response.headers);
  headers.delete(IMAGE_PASSTHROUGH);
  headers.set("vary", "accept");
  headers.set("cache-control", `public, max-age=${ttl}, must-revalidate`);
  headers.set(NEXT_CACHE_STATUS, "MISS");

  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}

const KEY_VERSION = 1;

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

function sourceIdentity(params: ImageParams, deps: ImageDeps): string {
  if (params.isAbsolute) return params.href;
  const path = assetPath(params.href, deps.basePath);
  const hash = path === undefined ? undefined : deps.assetHashes?.[path];
  return hash ?? `${deps.app}/${deps.deploymentId}${normalized(params.href)}`;
}

function assetPath(href: string, basePath: string): string | undefined {
  const parsed = parseUrl(href);
  if (!parsed) return undefined;
  const pathname = decode(parsed.pathname);
  if (pathname === undefined) return undefined;
  if (basePath && !pathname.startsWith(`${basePath}/`)) return undefined;
  return pathname.slice(basePath.length);
}

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

export const unprovisionedImageOrigin: ImageOrigin = async () =>
  new Response("No image optimizer is provisioned for this deployment.", {
    status: 502,
    headers: { "content-type": "text/plain; charset=utf-8" },
  });

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

const OPTIMIZER_STATUSES = new Set([400, 500, 502]);

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

function isImage(contentType: string | null): boolean {
  return contentType?.trimStart().toLowerCase().startsWith("image/") ?? false;
}

import {
  IMAGE_ERROR,
  IMAGE_PASSTHROUGH,
  type ImageConfig,
  type ImageOriginRequest,
  type ImageParams,
  type RouteImage,
  UPSTREAM_INVALID_MESSAGE,
  UPSTREAM_TIMEOUT_MS,
  validateImage,
} from "@framework/next-protocol/image";
import type { StorageCoordinate } from "@platform/edge-contract/storage-key";

import { mediaType } from "./accept.mjs";
import { deltaSeconds, headResponse, NEXT_CACHE_STATUS, withStatus } from "./http-cache.mjs";
import { retainOwner } from "./origin-response.mjs";

export type ImageOrigin = (payload: ImageOriginRequest) => Promise<Response>;

export interface ImageCall {
  payload: ImageOriginRequest;
  minimumCacheTTL: number;
}

export interface ImageCacheContext extends ImageCall {
  request: Request;
  digest: string;
  servedCacheControl?: string;
  origin: () => Promise<Response>;
}

export type ImageCache = (context: ImageCacheContext) => Promise<Response>;

export interface ImageDeps {
  config: ImageConfig;
  basePath: string;
  storage: StorageCoordinate;
  slug: string;
  app: string;
  appBuildId: string;
  origin: ImageOrigin;
  assetHashes?: Record<string, string>;
  imageCache?: ImageCache;
  route?: (request: Request) => Promise<Response>;
  maxRequestBytes?: number;
}

const DUMMY_ORIGIN = "http://n";

const INTEGER = /^[0-9]+$/;

export type ImageRequestValidation =
  | { ok: true; params: ImageRequestParams }
  | { ok: false; status: number; message: string };

export interface ImageRequestParams extends ImageParams {
  mimeType: string;
  isStatic: boolean;
}

function parseUrl(url: string): URL | undefined {
  try {
    return new URL(url, DUMMY_ORIGIN);
  } catch {
    return undefined;
  }
}

function parseAbsolute(url: string): URL | undefined {
  try {
    return new URL(url);
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

function param(url: URL, name: string): string | string[] | undefined {
  const values = url.searchParams.getAll(name);
  if (values.length === 0) return undefined;
  return values.length === 1 ? values[0] : values;
}

function integerParam(value: string | string[] | undefined): unknown {
  if (value === undefined || value === "" || Array.isArray(value)) return value;
  return INTEGER.test(value) ? parseInt(value, 10) : Number.NaN;
}

export function validateImageRequest(
  url: URL,
  accept: string,
  config: ImageConfig,
  basePath: string,
): ImageRequestValidation {
  const rawUrl = param(url, "url");
  const result = validateImage(
    { url: rawUrl, w: integerParam(param(url, "w")), q: integerParam(param(url, "q")) },
    config,
  );
  if (!result.ok) return result;
  return {
    ok: true,
    params: {
      ...result.params,
      mimeType: getSupportedMimeType(config.formats, accept),
      isStatic: STATIC_IMPORT_PREFIXES.some((prefix) =>
        (rawUrl as string).startsWith(`${basePath}${prefix}`),
      ),
    },
  };
}

const STATIC_IMPORT_PREFIXES = ["/_next/static/media", "/_next/static/immutable/media"];

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
  const payload: ImageOriginRequest = {
    storage: deps.storage,
    url: params.href,
    w: params.width,
    q: params.quality,
    accept,
    mimeType: params.mimeType,
    configHash: deps.config.configHash,
  };
  const route = isRouteSource(params, deps) ? deps.route : undefined;
  if (route) {
    const read = await readRouteImage(route, request, params.href, deps);
    if (!read.ok) return withStatus(errorResponse(read.status, read.message), "BYPASS");
    payload.routeImage = read.image;
    const refusal = refuseOversizeRequest(payload, params.href, deps.maxRequestBytes);
    if (refusal) return withStatus(errorResponse(refusal.status, refusal.message), "BYPASS");
  }
  const origin = async () => servedImage(await deps.origin(payload), deps.config.minimumCacheTTL);

  const context: ImageCacheContext = {
    request,
    digest: await imageDigest(params, deps),
    servedCacheControl: params.isStatic ? IMMUTABLE : undefined,
    payload,
    minimumCacheTTL: deps.config.minimumCacheTTL,
    origin,
  };
  return deps.imageCache && !route ? deps.imageCache(context) : serveUncachedImage(context);
}

function isRouteSource(params: ImageRequestParams, deps: ImageDeps): boolean {
  if (params.isAbsolute || !deps.route) return false;
  const path = assetPath(params.href, deps.basePath);
  return path === undefined || deps.assetHashes?.[path] === undefined;
}

interface ImageRefusal {
  status: number;
  message: string;
}

type RouteImageRead = { ok: true; image: RouteImage } | ({ ok: false } & ImageRefusal);

const PAYLOAD_TOO_LARGE = 413;

const UPSTREAM_INVALID: ImageRefusal = { status: 400, message: UPSTREAM_INVALID_MESSAGE };

const UPSTREAM_TOO_LARGE: ImageRefusal = {
  status: PAYLOAD_TOO_LARGE,
  message: UPSTREAM_INVALID_MESSAGE,
};

function newRequestTooLarge(maxRequestBytes: number): ImageRefusal {
  return {
    status: PAYLOAD_TOO_LARGE,
    message: `"url" parameter is valid but upstream response is too large for the image optimizer, which accepts requests of at most ${maxRequestBytes} bytes`,
  };
}

interface RouteBodyLimit {
  bytes: number;
  refusal: ImageRefusal;
}

function newRouteBodyLimit(deps: ImageDeps): RouteBodyLimit {
  const { maximumResponseBody } = deps.config;
  if (deps.maxRequestBytes === undefined) {
    return { bytes: maximumResponseBody, refusal: UPSTREAM_TOO_LARGE };
  }
  const encodable = Math.floor((deps.maxRequestBytes * 3) / 4);
  return encodable < maximumResponseBody
    ? { bytes: encodable, refusal: newRequestTooLarge(deps.maxRequestBytes) }
    : { bytes: maximumResponseBody, refusal: UPSTREAM_TOO_LARGE };
}

async function readRouteImage(
  route: (request: Request) => Promise<Response>,
  request: Request,
  href: string,
  deps: ImageDeps,
): Promise<RouteImageRead> {
  const routed = new Request(new URL(href, request.url), {
    signal: AbortSignal.timeout(UPSTREAM_TIMEOUT_MS),
  });
  try {
    const response = await route(routed);
    if (response.status !== 200) {
      await response.body?.cancel();
      console.error(`ocel image: route ${href} answered ${response.status}`);
      return { ok: false, ...UPSTREAM_INVALID };
    }
    const limit = newRouteBodyLimit(deps);
    const bytes = await readCapped(response, limit.bytes);
    if (!bytes) {
      console.error(`ocel image: route ${href} answered over ${limit.bytes} bytes`);
      return { ok: false, ...limit.refusal };
    }
    return {
      ok: true,
      image: {
        base64: encodeBase64(bytes),
        cacheControl: response.headers.get("cache-control"),
        etag: response.headers.get("etag"),
      },
    };
  } catch (error) {
    console.error(`ocel image: route ${href} failed`, error);
    return { ok: false, ...UPSTREAM_INVALID };
  }
}

function refuseOversizeRequest(
  payload: ImageOriginRequest,
  href: string,
  maxRequestBytes: number | undefined,
): ImageRefusal | undefined {
  if (maxRequestBytes === undefined) return undefined;
  const bytes = new TextEncoder().encode(JSON.stringify(payload)).byteLength;
  if (bytes <= maxRequestBytes) return undefined;
  console.error(
    `ocel image: route ${href} encodes to a ${bytes}-byte request, over the image optimizer's ${maxRequestBytes}`,
  );
  return newRequestTooLarge(maxRequestBytes);
}

async function readCapped(response: Response, limit: number): Promise<Uint8Array | undefined> {
  const declared = Number(response.headers.get("content-length") ?? Number.NaN);
  if (Number.isFinite(declared) && declared > limit) {
    await response.body?.cancel();
    return undefined;
  }
  const reader = response.body?.getReader();
  if (!reader) return new Uint8Array();
  const chunks: Uint8Array[] = [];
  let total = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    total += value.byteLength;
    if (total > limit) {
      await reader.cancel();
      return undefined;
    }
    chunks.push(value);
  }
  const bytes = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return bytes;
}

const BASE64_CHUNK = 0x8000;

function encodeBase64(bytes: Uint8Array): string {
  let binary = "";
  for (let i = 0; i < bytes.length; i += BASE64_CHUNK) {
    binary += String.fromCharCode(...bytes.subarray(i, i + BASE64_CHUNK));
  }
  return btoa(binary);
}

export async function serveUncachedImage(context: ImageCacheContext): Promise<Response> {
  return withServedCacheControl(withStatus(await context.origin(), "BYPASS"), context);
}

export function withServedCacheControl(response: Response, context: ImageCacheContext): Response {
  if (context.servedCacheControl && response.status === 200) {
    response.headers.set("cache-control", context.servedCacheControl);
  }
  return context.request.method === "HEAD" ? headResponse(response) : response;
}

const IMMUTABLE = "public, max-age=315360000, immutable";

export function servedImage(response: Response, minimumCacheTTL: number): Response {
  if (response.status !== 200) return withoutErrorMarker(response);

  const passthrough = response.headers.has(IMAGE_PASSTHROUGH);
  const upstream = passthrough
    ? 0
    : (deltaSeconds(response.headers.get("cache-control"), "s-maxage", "max-age") ?? 0);
  const ttl = Math.max(minimumCacheTTL, upstream);

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

function withoutErrorMarker(response: Response): Response {
  if (!response.headers.has(IMAGE_ERROR)) return response;
  const headers = new Headers(response.headers);
  headers.delete(IMAGE_ERROR);
  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}

const KEY_VERSION = 1;

async function imageDigest(params: ImageRequestParams, deps: ImageDeps): Promise<string> {
  return sha256(
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
}

function sourceIdentity(params: ImageRequestParams, deps: ImageDeps): string {
  if (params.isAbsolute) return params.href;
  const path = assetPath(params.href, deps.basePath);
  const hash = path === undefined ? undefined : deps.assetHashes?.[path];
  return hash ?? `${deps.app}/${deps.appBuildId}${normalized(params.href)}`;
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
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return [...new Uint8Array(digest)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
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
        retainOwner(
          await doFetch(url, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify(payload),
          }),
        ),
      );
    } catch {
      return unprovisionedImageOrigin(payload);
    }
  };
}

export const originImagePath = "/_ocel/image";

export function deploymentImageOrigin(
  entryUrl: string | undefined,
  doFetch: typeof fetch,
): ImageOrigin | undefined {
  if (!entryUrl || !parseAbsolute(entryUrl)) return undefined;
  return functionUrlImageOrigin(new URL(originImagePath, entryUrl).href, doFetch);
}

const OPTIMIZER_STATUSES = new Set([400, 500, 502]);

function relayed(response: Response): Response {
  const own =
    response.headers.has(IMAGE_ERROR) ||
    (response.status === 200
      ? isImage(response.headers.get("content-type"))
      : OPTIMIZER_STATUSES.has(response.status));
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

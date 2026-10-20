import type { StorageCoordinate } from "@platform/edge-contract/storage-key";
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

export interface ImageConfig extends CompiledImageConfig {
  configHash: string;
}

export interface ImageOriginRequest {
  storage: StorageCoordinate;
  url: string;
  w: number;
  q: number;
  accept: string;
  mimeType: string;
  configHash: string;
  routeImage?: RouteImage;
}

export interface RouteImage {
  base64: string;
  cacheControl: string | null;
  etag: string | null;
}

export const IMAGE_PASSTHROUGH = "x-ocel-image-passthrough";

export const IMAGE_ERROR = "x-ocel-image-error";

export const UPSTREAM_TIMEOUT_MS = 7_000;

export const UPSTREAM_INVALID_MESSAGE = '"url" parameter is valid but upstream response is invalid';

export type ImageRules = Pick<
  CompiledImageConfig,
  "deviceSizes" | "imageSizes" | "qualities" | "domains" | "remotePatterns" | "localPatterns"
>;

export interface ImageParams {
  href: string;
  width: number;
  quality: number;
  isAbsolute: boolean;
}

export type ImageValidation =
  | { ok: true; params: ImageParams }
  | { ok: false; status: number; message: string };

export interface ImageQuery {
  url: unknown;
  w: unknown;
  q: unknown;
}

const DUMMY_ORIGIN = "http://n";

const RECURSIVE = /\/_next\/image($|\/)/;

const MAX_URL_LENGTH = 3072;

const REGEXES = new Map<string, RegExp>();

function regex(source: string): RegExp {
  let compiled = REGEXES.get(source);
  if (!compiled) REGEXES.set(source, (compiled = new RegExp(source)));
  return compiled;
}

export function matchLocalPattern(pattern: CompiledLocalPattern, url: URL): boolean {
  if (pattern.search !== undefined && pattern.search !== url.search) return false;
  return regex(pattern.pathname).test(url.pathname);
}

export function matchRemotePattern(pattern: CompiledRemotePattern, url: URL): boolean {
  if (pattern.protocol !== undefined && pattern.protocol !== url.protocol.replace(/:$/, "")) {
    return false;
  }
  if (pattern.port !== undefined && pattern.port !== url.port) return false;
  if (!regex(pattern.hostname).test(url.hostname)) return false;
  if (pattern.search !== undefined && pattern.search !== url.search) return false;
  return regex(pattern.pathname).test(url.pathname);
}

export function isAllowedRemote(
  rules: Pick<ImageRules, "domains" | "remotePatterns">,
  url: URL,
): boolean {
  return (
    rules.domains.some((domain) => url.hostname === domain) ||
    rules.remotePatterns.some((pattern) => matchRemotePattern(pattern, url))
  );
}

function invalid(message: string): ImageValidation {
  return { ok: false, status: 400, message };
}

function malformed(): ImageValidation {
  return { ok: false, status: 500, message: "Internal Server Error" };
}

function parse(url: string, base?: string): URL | undefined {
  try {
    return new URL(url, base);
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

function isMissing(value: unknown): boolean {
  return value === undefined || value === null || value === "";
}

export function validateImage(query: ImageQuery, rules: ImageRules): ImageValidation {
  const { url, w, q } = query;

  if (isMissing(url)) return invalid('"url" parameter is required');
  if (Array.isArray(url)) return invalid('"url" parameter cannot be an array');
  if (typeof url !== "string") return invalid('"url" parameter is required');
  if (url.length > MAX_URL_LENGTH) return invalid('"url" parameter is too long');
  if (url.startsWith("//")) {
    return invalid('"url" parameter cannot be a protocol-relative URL (//)');
  }

  let href: string;
  let isAbsolute: boolean;
  if (url.startsWith("/")) {
    href = url;
    isAbsolute = false;
    const parsed = parse(url, DUMMY_ORIGIN);
    const pathname = decode(parsed?.pathname ?? "");
    if (pathname === undefined) return malformed();
    if (RECURSIVE.test(pathname)) return invalid('"url" parameter cannot be recursive');
    if (rules.localPatterns) {
      if (!parsed) return malformed();
      const allowed = rules.localPatterns.some((pattern) => matchLocalPattern(pattern, parsed));
      if (!allowed) return invalid('"url" parameter is not allowed');
    }
  } else {
    const parsed = parse(url);
    if (!parsed) return invalid('"url" parameter is invalid');
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return invalid('"url" parameter is invalid');
    }
    if (!isAllowedRemote(rules, parsed)) return invalid('"url" parameter is not allowed');
    href = parsed.toString();
    isAbsolute = true;
  }

  if (isMissing(w)) return invalid('"w" parameter (width) is required');
  if (Array.isArray(w)) return invalid('"w" parameter (width) cannot be an array');
  if (!Number.isInteger(w)) {
    return invalid('"w" parameter (width) must be an integer greater than 0');
  }

  if (isMissing(q)) return invalid('"q" parameter (quality) is required');
  if (Array.isArray(q)) return invalid('"q" parameter (quality) cannot be an array');
  if (!Number.isInteger(q)) {
    return invalid('"q" parameter (quality) must be an integer between 1 and 100');
  }

  const width = w as number;
  if (width <= 0) return invalid('"w" parameter (width) must be an integer greater than 0');
  if (![...rules.deviceSizes, ...rules.imageSizes].includes(width)) {
    return invalid(`"w" parameter (width) of ${width} is not allowed`);
  }

  const quality = q as number;
  if (quality < 1 || quality > 100) {
    return invalid('"q" parameter (quality) must be an integer between 1 and 100');
  }
  if (rules.qualities && !rules.qualities.includes(quality)) {
    return invalid(`"q" parameter (quality) of ${quality} is not allowed`);
  }

  return { ok: true, params: { href, width, quality, isAbsolute } };
}

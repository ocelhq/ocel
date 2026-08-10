import type { CompiledImageConfig, ImageOriginRequest } from "./contract.mjs";
import { isAllowedRemote, matchLocalPattern } from "./patterns.mjs";

export type Validation =
  | { ok: true; params: ValidParams }
  | { ok: false; status: number; message: string };

export interface ValidParams {
  href: string;
  width: number;
  quality: number;
  isAbsolute: boolean;
}

const DUMMY_ORIGIN = "http://n";

const RECURSIVE = /\/_next\/image($|\/)/;

function invalid(message: string): Validation {
  return { ok: false, status: 400, message };
}

function malformed(): Validation {
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

export function validate(
  payload: ImageOriginRequest,
  config: CompiledImageConfig,
): Validation {
  const { url, w, q } = payload;

  if (!url) return invalid('"url" parameter is required');
  if (Array.isArray(url)) return invalid('"url" parameter cannot be an array');
  if (typeof url !== "string") return invalid('"url" parameter is required');
  if (url.length > 3072) return invalid('"url" parameter is too long');
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
    const parsed = parse(url);
    if (!parsed) return invalid('"url" parameter is invalid');
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return invalid('"url" parameter is invalid');
    }
    if (!isAllowedRemote(config, parsed)) {
      return invalid('"url" parameter is not allowed');
    }
    href = parsed.toString();
    isAbsolute = true;
  }

  if (w === undefined || w === null) {
    return invalid('"w" parameter (width) is required');
  }
  if (Array.isArray(w)) return invalid('"w" parameter (width) cannot be an array');
  if (!Number.isInteger(w)) {
    return invalid('"w" parameter (width) must be an integer greater than 0');
  }

  if (q === undefined || q === null) {
    return invalid('"q" parameter (quality) is required');
  }
  if (Array.isArray(q)) {
    return invalid('"q" parameter (quality) cannot be an array');
  }
  if (!Number.isInteger(q)) {
    return invalid('"q" parameter (quality) must be an integer between 1 and 100');
  }

  if (w <= 0) {
    return invalid('"w" parameter (width) must be an integer greater than 0');
  }
  if (![...config.deviceSizes, ...config.imageSizes].includes(w)) {
    return invalid(`"w" parameter (width) of ${w} is not allowed`);
  }

  if (q < 1 || q > 100) {
    return invalid('"q" parameter (quality) must be an integer between 1 and 100');
  }
  if (config.qualities && !config.qualities.includes(q)) {
    return invalid(`"q" parameter (quality) of ${q} is not allowed`);
  }

  return { ok: true, params: { href, width: w, quality: q, isAbsolute } };
}

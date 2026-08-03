import type { CompiledImageConfig, ImageOriginRequest } from "./contract.mjs";
import { isAllowedRemote, matchLocalPattern } from "./patterns.mjs";

// The whole of Next's ImageOptimizerCache.validateParams, run again, here,
// against the config this function loaded from S3 itself.
//
// The edge already ran it. That is not a reason to skip it — it is the reason
// to repeat it. The edge validates against the config compiled into a routing
// manifest it was deployed with; this function validates against the artifact
// it fetched and hashed. If the two ever disagree — a stale worker, a replayed
// payload, a bug in either — the one holding the authority is this one, and
// tightening remotePatterns has to take effect even for a worker that has not
// noticed yet.
//
// The one shape difference from the edge's copy: the parameters arrive as JSON
// values rather than as query strings, so "is an array" and "is not an integer"
// are type checks here where they are regex checks there. The conditions and
// their order are otherwise the table verbatim, including the interleaving —
// both of q's presence checks run before w's value checks, so a request with a
// bad width and no quality is a quality error.

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

// A url that resolves to /_next/image is a request for this route to call
// itself. Matched against the decoded pathname and anywhere in it, never as a
// prefix: an assetPrefix in front of it, or a percent-encoded letter inside it,
// defeats a prefix check (CVE-2024-47831).
const RECURSIVE = /\/_next\/image($|\/)/;

function invalid(message: string): Validation {
  return { ok: false, status: 400, message };
}

// A url neither decodeURIComponent nor new URL can handle. Next throws here and
// its server answers a bare 500; the edge already reproduces that status rather
// than crashing, and so does this.
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

  // Falsy-only, as Next's own `!url` is. A non-empty array is truthy, so it
  // falls through to the row below — which is the whole reason the array row is
  // reachable at all. Testing `typeof url !== "string"` here instead swallows it
  // and answers "required" for an input Next names.
  if (!url) return invalid('"url" parameter is required');
  if (Array.isArray(url)) return invalid('"url" parameter cannot be an array');
  // Not a row in Next's table: a query string cannot produce a value that is
  // neither string nor array, but a JSON payload can. Answered as an absent url
  // rather than handed to a parser that assumes a string.
  if (typeof url !== "string") return invalid('"url" parameter is required');
  if (url.length > 3072) return invalid('"url" parameter is too long');
  // Before the relative/absolute branch, always: //evil.example is a host the
  // absolute branch would have checked and the relative branch would not, and
  // getting this order wrong is a whole-allowlist bypass (Next PR #65752).
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

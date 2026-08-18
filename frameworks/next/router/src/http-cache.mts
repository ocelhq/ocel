export const CACHE_STATUS = "x-ocel-cache";
export const NEXT_CACHE_STATUS = "x-nextjs-cache";
export const VERCEL_CACHE_STATUS = "x-vercel-cache";

export type CacheStatus = "HIT" | "PRERENDER" | "MISS" | "STALE" | "BYPASS";

export interface ResponseCache {
  match(request: Request): Promise<Response | undefined>;
  put(request: Request, response: Response): Promise<void>;
}

export interface CachePolicy {
  sMaxAge: number;
  swr: number;
}

export function directives(cacheControl: string | null): Map<string, string> {
  const parsed = new Map<string, string>();
  if (!cacheControl) return parsed;
  for (const part of cacheControl.split(",")) {
    const [name = "", value = ""] = part.trim().toLowerCase().split("=");
    parsed.set(name, value);
  }
  return parsed;
}

export function deltaSeconds(
  cacheControl: string | null,
  ...names: string[]
): number | undefined {
  const parsed = directives(cacheControl);
  for (const name of names) {
    if (!parsed.has(name)) continue;
    const value = Number(parsed.get(name));
    if (Number.isFinite(value) && value >= 0) return value;
  }
  return undefined;
}

export function storagePolicy(cacheControl: string | null): CachePolicy | null {
  if (!cacheControl) return null;

  const parsed = directives(cacheControl);
  if (
    parsed.has("no-store") ||
    parsed.has("no-cache") ||
    parsed.has("private")
  ) {
    return null;
  }

  const sMaxAge = Number(parsed.get("s-maxage"));
  if (!Number.isFinite(sMaxAge) || sMaxAge <= 0) return null;

  const swr = Number(parsed.get("stale-while-revalidate") ?? 0);
  return { sMaxAge, swr: Number.isFinite(swr) && swr > 0 ? swr : 0 };
}

export function respond(response: Response, headers: Headers): Response {
  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}

export function withStatus(response: Response, status: CacheStatus): Response {
  const headers = new Headers(response.headers);
  headers.set(CACHE_STATUS, status);
  return respond(response, headers);
}

export function withVercelCacheAlias(
  response: Response,
  enabled: boolean | undefined,
): Response {
  if (!enabled) return response;
  const status = response.headers.get(CACHE_STATUS);
  if (status === null) return response;
  const aliased = new Response(response.body, response);
  aliased.headers.set(VERCEL_CACHE_STATUS, status);
  return aliased;
}

export function headResponse(response: Response): Response {
  response.body?.cancel();
  return new Response(null, {
    status: response.status,
    statusText: response.statusText,
    headers: response.headers,
  });
}

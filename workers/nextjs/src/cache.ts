// The edge cache for prerendered routes.
//
// Cloudflare's Cache API does not honor stale-while-revalidate, so freshness is
// computed here against a timestamp we store ourselves, and entries are stored
// under a rewritten Cache-Control that spans the whole stale window so the zone
// retains them long enough for us to serve stale. That rewritten directive is
// internal: the served copy always carries the origin's own Cache-Control,
// because a browser that caches our internal TTL cannot be purged.

const ORIGIN_CACHE_CONTROL = "x-ocel-origin-cache-control";
const STORED_AT = "x-ocel-stored-at";
const CACHE_STATUS = "x-ocel-cache";
const DRAFT_COOKIE = "__prerender_bypass";

export type CacheStatus = "HIT" | "STALE" | "MISS" | "BYPASS";

export interface CacheDeps {
  cache: Cache;
  waitUntil: (promise: Promise<unknown>) => void;
  // Injected so freshness never depends on wall-clock time. Milliseconds.
  now?: () => number;
}

export interface CacheTarget {
  key: string;
  tags?: string[];
}

export interface CachePolicy {
  sMaxAge: number;
  swr: number;
}

// storagePolicy returns null for anything we must not store. It gates the
// Cache-Control rewrite below: rewriting first would turn a response the origin
// marked uncacheable into a cacheable one.
export function storagePolicy(cacheControl: string | null): CachePolicy | null {
  if (!cacheControl) return null;

  const directives = new Map<string, string>();
  for (const part of cacheControl.split(",")) {
    const [name, value = ""] = part.trim().toLowerCase().split("=");
    directives.set(name, value);
  }

  if (
    directives.has("no-store") ||
    directives.has("no-cache") ||
    directives.has("private")
  ) {
    return null;
  }

  const sMaxAge = Number(directives.get("s-maxage"));
  if (!Number.isFinite(sMaxAge) || sMaxAge <= 0) return null;

  const swr = Number(directives.get("stale-while-revalidate") ?? 0);
  return { sMaxAge, swr: Number.isFinite(swr) && swr > 0 ? swr : 0 };
}

export function freshness(
  ageSeconds: number,
  policy: CachePolicy,
): "fresh" | "stale" | "expired" {
  if (ageSeconds < policy.sMaxAge) return "fresh";
  if (ageSeconds < policy.sMaxAge + policy.swr) return "stale";
  return "expired";
}

// cacheKey scopes an entry to one build: the zone cache outlives a script
// update, so a key built from request.url would serve the previous build's HTML
// for as long as its TTL. Query params survive only if the route allows them —
// an absent allowQuery is no rule at all, which is not the same as [] (drop
// everything), so the full query is kept.
export function cacheKey(
  buildId: string,
  identity: string,
  url: URL,
  allowQuery?: string[],
): string {
  const key = new URL(`https://cache.ocel/${buildId}${identity}`);
  const names = [
    ...new Set(allowQuery ?? [...url.searchParams.keys()]),
  ].sort();

  for (const name of names) {
    for (const value of url.searchParams.getAll(name)) {
      key.searchParams.append(name, value);
    }
  }
  return key.toString();
}

function hasDraftCookie(request: Request): boolean {
  const cookie = request.headers.get("cookie");
  return cookie !== null && new RegExp(`(?:^|;\\s*)${DRAFT_COOKIE}=`).test(cookie);
}

function withStatus(response: Response, status: CacheStatus): Response {
  const headers = new Headers(response.headers);
  headers.set(CACHE_STATUS, status);
  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}

function forStorage(
  response: Response,
  policy: CachePolicy,
  target: CacheTarget,
  storedAt: number,
): Response {
  const headers = new Headers(response.headers);
  headers.set(ORIGIN_CACHE_CONTROL, response.headers.get("cache-control") ?? "");
  headers.set(STORED_AT, String(storedAt));
  headers.set("cache-control", `s-maxage=${policy.sMaxAge + policy.swr}`);
  if (target.tags?.length) headers.set("cache-tag", target.tags.join(","));

  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}

function fromStorage(response: Response, status: CacheStatus): Response {
  const headers = new Headers(response.headers);
  headers.set("cache-control", headers.get(ORIGIN_CACHE_CONTROL) ?? "");
  headers.delete(ORIGIN_CACHE_CONTROL);
  headers.delete(STORED_AT);
  headers.delete("cache-tag");
  headers.set(CACHE_STATUS, status);

  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}

async function store(
  keyRequest: Request,
  target: CacheTarget,
  deps: CacheDeps,
  response: Response,
): Promise<void> {
  if (response.status !== 200) return;
  const policy = storagePolicy(response.headers.get("cache-control"));
  if (!policy) return;

  const now = deps.now ?? Date.now;
  await deps.cache.put(keyRequest, forStorage(response, policy, target, now()));
}

// serveCached is the whole cache surface: the caller decides only whether a
// route is cacheable at all, and hands us a way to reach the origin.
export async function serveCached(
  request: Request,
  target: CacheTarget,
  deps: CacheDeps,
  origin: () => Promise<Response>,
): Promise<Response> {
  // Our key is a synthetic GET, so the Cache API's own GET-only rule would let a
  // POST match the cached document. Draft mode must miss the public entry
  // entirely, in both directions, or it silently stops working.
  if (request.method !== "GET" || hasDraftCookie(request)) {
    return withStatus(await origin(), "BYPASS");
  }

  const keyRequest = new Request(target.key);
  const now = deps.now ?? Date.now;
  const cached = await deps.cache.match(keyRequest);

  if (cached) {
    const policy = storagePolicy(cached.headers.get(ORIGIN_CACHE_CONTROL));
    const storedAt = Number(cached.headers.get(STORED_AT));

    if (policy && Number.isFinite(storedAt)) {
      const state = freshness((now() - storedAt) / 1000, policy);
      if (state === "fresh") return fromStorage(cached, "HIT");
      if (state === "stale") {
        // A refresh that fails leaves the existing entry in place rather than
        // replacing it with an error.
        deps.waitUntil(
          origin()
            .then((response) => store(keyRequest, target, deps, response))
            .catch(() => {}),
        );
        return fromStorage(cached, "STALE");
      }
    }
  }

  const response = await origin();
  deps.waitUntil(store(keyRequest, target, deps, response.clone()).catch(() => {}));
  return withStatus(response, "MISS");
}

// The edge cache logic for prerendered routes.
const ORIGIN_CACHE_CONTROL = "x-ocel-origin-cache-control";
const STORED_AT = "x-ocel-stored-at";

// The one status header every served route carries, reporting which tier
// answered: HIT (this colo's cache), PRERENDER (the R2 ISR store, one tier
// down), MISS (neither — the Lambda origin rendered it), or BYPASS. A colo
// serve is a HIT whether the entry was fresh or served stale-while-revalidate;
// staleness drives the background refresh, not the header.
export const CACHE_STATUS = "x-ocel-cache";
const DRAFT_COOKIE = "__prerender_bypass";

export type CacheStatus = "HIT" | "PRERENDER" | "MISS" | "BYPASS";

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

export type CacheKeyResult =
  | { cacheable: true; key: string }
  | { cacheable: false };

// variantPath maps a request to the synthetic pathname that names its response
// variant — the closed, self-describing set of shapes a prerendered route can
// answer with. It returns null for a per-visitor dynamic variant, which must
// never enter a shared cache. This replaces Next's `_rsc` cache-buster: we own
// the Worker, so we key on the vary headers directly rather than on their hash.
export function variantPath(
  pathname: string,
  headers: Headers,
  renderingMode: "STATIC" | "PARTIALLY_STATIC" | undefined,
): string | null {
  if (headers.get("RSC") === null) return pathname; // HTML document / shell.

  // A segment prefetch names one prerendered segment; it is static build output
  // regardless of the prefetch value.
  const segment = headers.get("next-router-segment-prefetch");
  if (segment !== null) {
    return `${pathname}.segments/${encodeURIComponent(segment)}.segment.rsc`;
  }

  // Only next-router-prefetch: 1 is a static full-route prefetch; 2/3 are
  // runtime prefetches that intentionally perform a dynamic request.
  const prefetch = headers.get("next-router-prefetch");
  if (prefetch === "1") return `${pathname}.prefetch.rsc`;
  if (prefetch !== null) return null;

  // Bare RSC with no prefetch is a cacheable payload only on a fully static
  // route; on a PPR route it is the dynamic navigation that fills the holes.
  return renderingMode === "STATIC" ? `${pathname}.rsc` : null;
}

export function cacheKey(
  buildId: string,
  pathname: string,
  url: URL,
  headers: Headers,
  renderingMode: "STATIC" | "PARTIALLY_STATIC" | undefined,
  allowQuery?: string[],
): CacheKeyResult {
  const variant = variantPath(pathname, headers, renderingMode);
  if (variant === null) return { cacheable: false };

  const key = new URL(`https://cache.ocel/${buildId}${variant}`);
  // `_rsc` is Next's cache-buster for path-keyed CDNs; we key on the headers it
  // hashes instead, so it is always dropped from our key. The browser still
  // sends it and the origin still receives it — this only excludes it here.
  const names = (allowQuery ?? [...url.searchParams.keys()]).filter(
    (name) => name !== "_rsc",
  );

  for (const name of [...names].sort()) {
    for (const value of url.searchParams.getAll(name)) {
      key.searchParams.append(name, value);
    }
  }

  return { cacheable: true, key: key.toString() };
}

export function hasDraftCookie(request: Request): boolean {
  const cookie = request.headers.get("cookie");
  return (
    cookie !== null && new RegExp(`(?:^|;\\s*)${DRAFT_COOKIE}=`).test(cookie)
  );
}

export function withStatus(response: Response, status: CacheStatus): Response {
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
  headers.set(
    ORIGIN_CACHE_CONTROL,
    response.headers.get("cache-control") ?? "",
  );
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

  // this is what is forwarded to browser
  headers.set("cache-control", "public, max-age=0, must-revalidate");
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
  if (response.status !== 200) {
    response.body?.cancel();
    return;
  }

  const policy = storagePolicy(response.headers.get("cache-control"));

  if (!policy) {
    response.body?.cancel();
    return;
  }

  const now = deps.now ?? Date.now;
  await deps.cache.put(keyRequest, forStorage(response, policy, target, now()));
}

// Every request arriving on a stale entry would otherwise start its own origin
// render, so one isolate can put a burst of identical regenerations on a single
// Lambda. Keyed by the cache object, which is one stable instance per isolate,
// so the in-flight set neither leaks between isolates nor between tests.
const inFlight = new WeakMap<Cache, Map<string, Promise<unknown>>>();

export function refreshOnce(
  deps: CacheDeps,
  key: string,
  run: () => Promise<unknown>,
): void {
  let pending = inFlight.get(deps.cache);
  if (!pending) inFlight.set(deps.cache, (pending = new Map()));
  if (pending.has(key)) return;

  const promise = run()
    .catch(() => {})
    .finally(() => pending.delete(key));
  pending.set(key, promise);
  deps.waitUntil(promise);
}

export async function serveCached(
  request: Request,
  target: CacheTarget,
  deps: CacheDeps,
  origin: () => Promise<Response>,
  originBlocking: () => Promise<Response>
): Promise<Response> {
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
        // A colo serve is a HIT even when stale; serving stale is what triggers
        // the background refresh, which forces a blocking origin render so the
        // entry is rewritten fresh for the next request.
        refreshOnce(deps, target.key, () =>
          originBlocking().then((response) =>
            store(keyRequest, target, deps, response),
          ),
        );

        return fromStorage(cached, "HIT");
      }
    }
  }

  const response = await origin();

  deps.waitUntil(
    store(keyRequest, target, deps, response.clone()).catch(() => {}),
  );

  // The origin is either the R2 ISR store, which stamps PRERENDER, or the
  // Lambda, which stamps nothing — and an unstamped response is a MISS.
  const served: CacheStatus =
    response.headers.get(CACHE_STATUS) === "PRERENDER" ? "PRERENDER" : "MISS";
  const result = withStatus(response, served);
  // client must always revalidate - no browser cache
  result.headers.set("cache-control", "public, max-age=0, must-revalidate");
  return result;
}

import { resolveRoutes } from "@next/routing";
import {
  CacheDeps,
  cacheKey,
  hasDraftCookie,
  refreshOnce,
  serveCached,
  withStatus,
} from "./cache";
import { composePpr, resumeRequest } from "./ppr";
import {
  intercept,
  type InterceptDeps,
  type InterceptionConfig,
} from "./interception";

// The request headers a Next App Router response varies on. The colo cache key
// is derived from these directly (see variantPath), and Next's own allowHeader
// for a prerender omits them — so the origin still needs them forwarded to
// render the right variant on a cache miss.
const RSC_FORWARD_HEADERS = new Set([
  "rsc",
  "next-router-prefetch",
  "next-router-state-tree",
  "next-router-segment-prefetch",
  "next-url",
]);

interface Env {
  ASSETS: Fetcher;
  FUNCTION_URLS: string;
  // Bound only where the edge provisioned a cache store; together with the build
  // prefix, its presence is what lets the worker read the ISR cache directly.
  OCEL_CACHE_STORE?: R2Bucket;
  OCEL_ISR_PREFIX?: string;
}

type RouteHas =
  | {
      type: "header" | "cookie" | "query";
      key: string;
      value?: string;
    }
  | {
      type: "host";
      key?: undefined;
      value: string;
    };

type DispatchTarget =
  | { kind: "static" }
  | { kind: "lambda"; id: string; parent?: string; revalidate?: unknown }
  | {
      kind: "prerender";
      id: string;
      tags?: string[];
      allowQuery?: string[];
      fallback?: {
        initialExpiration?: number;
        initialRevalidate?: number | false;
      };
      // The headers the build declares for this route's resume request, read
      // from the manifest rather than assumed.
      pprChain?: { headers: Record<string, string> };
      config: {
        allowQuery?: string[];
        allowHeader?: string[];
        bypassFor?: RouteHas[];
        renderingMode?: "STATIC" | "PARTIALLY_STATIC";
        partialFallback?: boolean;
        bypassToken?: string;
      };
    }
  | { kind: "edge"; entryKey?: string };

interface Manifest {
  buildId: string;
  basePath: string;
  pathnames: string[];
  routes: unknown;
  dispatch: Record<string, DispatchTarget>;
}

// The relevant subset of resolveRoutes' result; typed loosely so the dispatch
// logic can be exercised with synthetic results in tests.
interface RouteResult {
  middlewareResponded?: boolean;
  status?: number;
  redirect?: { url: URL | string; status: number };
  externalRewrite?: string | URL;
  resolvedPathname?: string | null;
  invocationTarget?: { pathname: string } | null;
}

export interface RouteDeps {
  manifest: Manifest;
  functionUrls: Record<string, string>;
  // The Workers Assets binding (or any Fetcher) serving this app's static output.
  assets: Pick<Fetcher, "fetch">;
  // Injectable so lambda/external forwarding can be observed in tests.
  fetch?: typeof fetch;

  // Absent outside a Worker request (and in routing tests): routes then forward
  // to their origin uncached.
  cache?: CacheDeps;

  // Present when the deploy bound a cache store and injected its prefix:
  // prerender routes then read the authoritative ISR cache directly from the
  // store before falling open to the Lambda origin. Absent leaves the Lambda
  // path unchanged.
  interception?: Pick<
    InterceptDeps,
    "store" | "snapshotCache" | "now" | "waitUntil"
  > & {
    config: InterceptionConfig;
  };
}

export async function dispatchResult(
  result: RouteResult,
  request: Request,
  deps: RouteDeps,
): Promise<Response> {
  const { manifest, functionUrls, assets } = deps;
  const doFetch = deps.fetch ?? fetch;
  const url = new URL(request.url);

  request = await bufferBody(request);

  if (result.middlewareResponded) {
    return new Response(null, { status: result.status ?? 200 });
  }
  if (result.redirect) {
    return Response.redirect(
      result.redirect.url.toString(),
      result.redirect.status,
    );
  }
  if (result.externalRewrite) {
    return doFetch(new Request(result.externalRewrite, request));
  }
  if (!result.resolvedPathname) {
    return assetsOr404(request, assets);
  }

  const target = manifest.dispatch[result.resolvedPathname];
  if (!target) {
    // Not in the manifest — fall back to the Assets binding before giving up,
    // so any file present in static/ is still served even if unenumerated.
    return assetsOr404(request, assets);
  }

  switch (target.kind) {
    case "static":
      // Workers Assets serves _next/static, public/, and the other truly-static
      // files. Use the ORIGINAL request so the asset path matches.
      return assetsOr404(request, assets);

    case "lambda": {
      const fnUrl = functionUrls[target.id];
      if (!fnUrl) return noFunctionUrl(target.id);
      return doFetch(
        forward(originUrl(fnUrl, url, result), request, request.headers),
      );
    }

    case "prerender": {
      const fnUrl = functionUrls[target.id];
      if (!fnUrl) return noFunctionUrl(target.id);
      return dispatchPrerender(request, url, result, target, fnUrl, deps);
    }

    case "edge":
      // TODO: edge functions run in-Worker; import the compiled edge entry by
      // target.entryKey and invoke it.
      return new Response("Edge runtime not wired yet", { status: 501 });

    default:
      return assetsOr404(request, assets);
  }
}

type PrerenderTarget = Extract<DispatchTarget, { kind: "prerender" }>;

// dispatchPrerender serves a prerendered route: from the colo cache when it can,
// from the ISR cache the worker reads itself when edge coordinates are present,
// and from the Lambda whenever neither can answer.
async function dispatchPrerender(
  request: Request,
  url: URL,
  result: RouteResult,
  target: PrerenderTarget,
  fnUrl: string,
  deps: RouteDeps,
): Promise<Response> {
  const doFetch = deps.fetch ?? fetch;
  const forwardUrl = originUrl(fnUrl, url, result);

  if (!deps.cache) {
    return doFetch(forward(forwardUrl, request, request.headers));
  }

  if (shouldBypass(request, url, target.config)) {
    const response = await doFetch(forward(forwardUrl, request, request.headers));
    return withStatus(response, "BYPASS");
  }

  const safeHeaders = new Headers();
  const allowedHeaders = target.config.allowHeader?.map((h) => h.toLowerCase());
  for (const [name, value] of request.headers) {
    const lower = name.toLowerCase();
    if (allowedHeaders?.includes(lower) || RSC_FORWARD_HEADERS.has(lower)) {
      safeHeaders.set(name, value);
    }
  }

  const origin = () => doFetch(forward(forwardUrl, request, safeHeaders));

  const blockingHeaders = new Headers(safeHeaders);
  blockingHeaders.set("x-prerender-revalidate", target.config.bypassToken ?? "");
  const originBlocking = () =>
    doFetch(forward(forwardUrl, request, blockingHeaders));

  // A pages-router data request (/_next/data/<build>/route.json) resolves to
  // the same prerender target as its html route, but must be answered with
  // JSON pageData, not html. Interception reconstructs only the html/RSC
  // variants, so those requests fall open to the Lambda exactly as today.
  const isNextData =
    url.pathname.startsWith((deps.manifest.basePath ?? "") + "/_next/data/") &&
    url.pathname.endsWith(".json");

  const routePath = result.invocationTarget?.pathname ?? url.pathname;
  const keyResult = cacheKey(
    deps.manifest.buildId,
    url.pathname,
    url,
    request.headers,
    target.config.renderingMode,
    target.allowQuery,
  );
  // A stable per-route id for deduping background revalidations, independent of
  // whether this particular variant is colo-cacheable.
  const refreshKey = `${deps.manifest.buildId}:${routePath}`;

  // When edge cache coordinates are present, a prerender read is tried
  // directly against the cache first; any miss/expiry/error falls open to
  // the Lambda origin. A complete interception hit carries the entry's
  // remaining window so serveCached memoizes it exactly as it would the
  // Lambda's response.
  let cachingOrigin = origin;
  if (deps.interception && !isNextData) {
    const { config, ...interceptDeps } = deps.interception;
    const read = once(() =>
      intercept(
        request,
        {
          routePath,
          fallbackPath: result.resolvedPathname ?? undefined,
          revalidate: target.fallback?.initialRevalidate,
          expiration: target.fallback?.initialExpiration,
        },
        config,
        interceptDeps,
      ),
    );

    // A composed PPR response is rendered for one visitor and must not reach
    // serveCached, so a route that might postpone is read before the colo cache
    // is consulted. A STATIC route cannot postpone, so its read stays behind
    // the cache, where a hit costs no store read at all.
    const mayPostpone =
      target.config.renderingMode !== "STATIC" &&
      request.method === "GET" &&
      !hasDraftCookie(request);

    if (mayPostpone) {
      const hit = await read();
      if (hit?.kind === "ppr") {
        if (hit.stale) {
          refreshOnce(deps.cache, refreshKey, async () =>
            (await originBlocking()).body?.cancel(),
          );
        }
        return composePpr(
          hit,
          doFetch(
            resumeRequest(
              forwardUrl,
              request,
              hit.postponed,
              target.pprChain?.headers,
            ),
          ),
        );
      }
    }

    cachingOrigin = async () => {
      const hit = await read();
      // A complete entry answered from the R2 store is a PRERENDER serve;
      // serveCached preserves that tier and memoizes the response so the next
      // request is a colo HIT. A miss falls open to the Lambda, an unstamped
      // MISS.
      return hit?.kind === "complete"
        ? withStatus(hit.response, "PRERENDER")
        : origin();
    };
  }

  if (!keyResult.cacheable) {
    // A per-visitor dynamic variant (PPR navigation, runtime prefetch): never
    // colo-cached. It goes straight to the Lambda under the same filtered
    // headers a prerender miss uses today.
    return withStatus(await origin(), "MISS");
  }

  return serveCached(
    request,
    { key: keyResult.key, tags: target.tags },
    deps.cache,
    cachingOrigin,
    originBlocking,
  );
}

function once<T>(run: () => Promise<T>): () => Promise<T> {
  let pending: Promise<T> | undefined;
  return () => (pending ??= run());
}

// originUrl points a request at its Function URL, preferring the routing
// result's invocation target so a rewritten path reaches the right handler.
function originUrl(fnUrl: string, url: URL, result: RouteResult): URL {
  const pathname = result.invocationTarget?.pathname ?? url.pathname;
  return new URL(pathname + url.search, fnUrl);
}

// bufferBody reads a request's body into memory so every forward of it carries a
// concrete Content-Length instead of a re-streamed (chunked) body. An AWS Lambda
// Function URL rejects a chunked request body with a 502 before the function ever
// runs — which flaps, because whether Cloudflare buffers a small body or streams
// it is nondeterministic. Buffering here is what the PPR resume already does for
// its own POST; doing it for the served request makes forwarded actions reliable.
async function bufferBody(request: Request): Promise<Request> {
  if (!request.body || request.method === "GET" || request.method === "HEAD") {
    return request;
  }
  const body = await request.arrayBuffer();
  return new Request(request.url, {
    method: request.method,
    headers: request.headers,
    body,
    redirect: "manual",
  });
}

// forward rebuilds a request against an origin URL under a chosen header set,
// keeping the method and body of the request being served.
//
// The origin sits behind a Function URL, so its `host` is that URL's host, not
// the public one the browser addressed. Next's Server Action CSRF check compares
// the `origin` header against `x-forwarded-host` (falling back to `host`), so the
// public host is stamped here — as the reverse proxy, this worker is authoritative
// for it — or every forwarded action would abort on a host/origin mismatch.
export function forward(
  url: URL,
  request: Request,
  headers: HeadersInit,
  body: BodyInit | null = request.body,
): Request {
  const publicUrl = new URL(request.url);
  const forwarded = new Headers(headers);
  forwarded.set("x-forwarded-host", publicUrl.host);
  forwarded.set("x-forwarded-proto", publicUrl.protocol.replace(/:$/, ""));
  return new Request(url, {
    method: request.method,
    headers: forwarded,
    body,
    redirect: "manual",
  });
}

function noFunctionUrl(id: string): Response {
  return new Response(`No function URL for ${id}`, { status: 502 });
}

// shouldBypass decides whether a prerender request must skip the cache and go
// straight to the origin: the route's own revalidate token, or any one of its
// bypassFor conditions. Next builds bypassFor as independent bypass *reasons*
// (server action, multipart upload, bot), so they OR — ANDing them could never
// match.
export function shouldBypass(
  request: Request,
  url: URL,
  config: { bypassFor?: RouteHas[]; bypassToken?: string },
): boolean {
  if (
    config.bypassToken &&
    request.headers.get("x-prerender-revalidate") === config.bypassToken
  ) {
    return true;
  }
  return (config.bypassFor ?? []).some((has) => matchesHas(has, request, url));
}

// matchesHas mirrors Next's own hasMatch: a bare condition matches on presence
// of a truthy value, and a condition with a value matches it as an ANCHORED
// regex — not a string equality. A repeated key is matched on its last value.
function matchesHas(has: RouteHas, request: Request, url: URL): boolean {
  const value = hasValue(has, request, url);
  if (!value) return false;
  if (!has.value) return true;

  const candidate = Array.isArray(value) ? value[value.length - 1] : value;
  try {
    return new RegExp(`^${has.value}$`).test(candidate);
  } catch {
    return false;
  }
}

function hasValue(
  has: RouteHas,
  request: Request,
  url: URL,
): string | string[] | undefined {
  switch (has.type) {
    case "header":
      return request.headers.get(has.key) ?? undefined;
    case "cookie":
      return cookieValue(request.headers.get("cookie"), has.key);
    case "query": {
      const values = url.searchParams.getAll(has.key);
      if (values.length === 0) return undefined;
      return values.length === 1 ? values[0] : values;
    }
    case "host":
      // The port is not part of the host a route condition names.
      return url.host.split(":", 1)[0].toLowerCase();
  }
}

function cookieValue(header: string | null, key: string): string | undefined {
  for (const part of header?.split(";") ?? []) {
    const eq = part.indexOf("=");
    if (eq > 0 && part.slice(0, eq).trim() === key) {
      return part.slice(eq + 1).trim();
    }
  }
  return undefined;
}

// assetsOr404 serves a request from the Assets binding, mapping the binding's
// own 404 to a plain 404 so callers can treat a miss uniformly.
async function assetsOr404(
  request: Request,
  assets: Pick<Fetcher, "fetch">,
): Promise<Response> {
  const res = await assets.fetch(request);
  return res.status === 404 ? new Response("Not Found", { status: 404 }) : res;
}

export default {
  async fetch(request, env, ctx): Promise<Response> {
    // The routing manifest is uploaded alongside the worker as a text module
    // (Cloudflare's module upload has no JSON type), so its default export is the
    // raw JSON string. The variable specifier keeps esbuild from trying to inline
    // a file that only exists at deploy time.
    const specifier = "./routing-manifest.json";
    const manifest = JSON.parse((await import(specifier)).default) as Manifest;

    const result = (await resolveRoutes({
      url: new URL(request.url),
      buildId: manifest.buildId,
      basePath: manifest.basePath,
      i18n: undefined,
      headers: request.headers,
      requestBody: request.body as ReadableStream,
      pathnames: manifest.pathnames,
      routes: manifest.routes as Parameters<typeof resolveRoutes>[0]["routes"],

      // TODO: invoke user-defined middleware
      invokeMiddleware: async () => {
        return {};
      },
    })) as RouteResult;

    // Interception is enabled only where both the cache store is bound and its
    // build prefix is injected; either missing leaves prerender routes forwarding
    // to the Lambda exactly as before.
    const store = env.OCEL_CACHE_STORE;
    const prefix = env.OCEL_ISR_PREFIX;

    return dispatchResult(result, request, {
      manifest,
      functionUrls: JSON.parse(env.FUNCTION_URLS) as Record<string, string>,
      assets: env.ASSETS,
      fetch,
      cache: {
        cache: caches.default,
        waitUntil: (promise) => ctx.waitUntil(promise),
      },
      interception:
        store && prefix
          ? {
              config: { prefix },
              // Passed as the binding itself: it is one stable object per
              // isolate, which is what the snapshot memo keys on.
              store,
              snapshotCache: caches.default,
              waitUntil: (promise) => ctx.waitUntil(promise),
            }
          : undefined,
    });
  },
} satisfies ExportedHandler<Env>;

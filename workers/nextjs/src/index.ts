import { resolveRoutes } from "@next/routing";

import { type CacheDeps, cacheKey, serveCached } from "./cache";

interface Env {
  ASSETS: Fetcher;
  FUNCTION_URLS: string;
}

type DispatchTarget =
  | { kind: "static" }
  | { kind: "lambda"; id: string; parent?: string; revalidate?: unknown }
  // A prerendered route. Its config + fallback live in the asset bucket keyed
  // by build id; until the ISR cache path lands the worker just invokes the
  // parent function (id) to render on every request, so the route still works.
  //
  // tags are the route's Cloudflare-legal cache tags (the only purge path once
  // the edge cache lands) and allowQuery the query params that belong in its
  // cache key. Both are baked in by the adapter — the origin response carries
  // neither. Optional: a manifest from a build before they existed has neither,
  // and an empty allowQuery ([] — drop all query) is not the same as no rule.
  | { kind: "prerender"; id: string; tags?: string[]; allowQuery?: string[] }
  | { kind: "edge"; entryKey?: string };

// The RSC block of the routes manifest. Next's adapter derives the .rsc output
// pathnames from these same values, so resolution here must mirror it.
interface RscConfig {
  header: string;
  suffix: string;
  prefetchSegmentHeader: string;
  prefetchSegmentSuffix: string;
  prefetchSegmentDirSuffix: string;
}

interface Manifest {
  buildId: string;
  basePath: string;
  pathnames: string[];
  routes: { rsc?: RscConfig };
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
  // The Workers Assets binding (or any Fetcher) serving .ocel/output/static.
  assets: Pick<Fetcher, "fetch">;
  // Injectable so lambda/external forwarding can be observed in tests.
  fetch?: typeof fetch;
  // Absent outside a Worker request (and in routing tests): routes then forward
  // to their origin uncached.
  cache?: CacheDeps;
}

export interface RscResolution {
  // The dispatch (and, later, cache) identity for this request. Never forwarded
  // to the origin — see dispatchResult.
  pathname: string;
  // False when an RSC variant was requested but has no dispatch entry, so the
  // request falls back to an identity it shares with the document response.
  // Cloudflare ignores Vary, so caching on that shared identity would serve RSC
  // payloads to document requests.
  cacheEligible: boolean;
}

// Mirrors Next's normalizePagePath: the adapter names RSC outputs after the
// page file, so '/' is '/index' and a literal '/index' route doubles.
function normalizePagePath(pathname: string): string {
  if (pathname === "/") return "/index";
  return /^\/index(\/|$)/.test(pathname) ? `/index${pathname}` : pathname;
}

function rscVariantPathname(
  pathname: string,
  headers: Headers,
  rsc: RscConfig,
): string | undefined {
  const base = normalizePagePath(pathname);

  const segment = headers.get(rsc.prefetchSegmentHeader);
  if (segment) {
    const dir = base + rsc.prefetchSegmentDirSuffix;
    const path = segment.startsWith("/") ? segment : `/${segment}`;
    return dir + path + rsc.prefetchSegmentSuffix;
  }

  if (headers.has(rsc.header)) return base + rsc.suffix;
  return undefined;
}

// resolveRsc gives an RSC or segment-prefetch request its own identity, because
// @next/routing resolves both to the bare pathname the document uses. Vercel
// separates these variants by pathname rather than by Vary, and the manifest's
// dispatch entries are keyed that way.
export function resolveRsc(
  pathname: string,
  headers: Headers,
  manifest: Manifest,
): RscResolution {
  const rsc = manifest.routes?.rsc;
  if (!rsc) return { pathname, cacheEligible: true };

  const variant = rscVariantPathname(pathname, headers, rsc);
  if (!variant) return { pathname, cacheEligible: true };
  if (manifest.dispatch[variant]) {
    return { pathname: variant, cacheEligible: true };
  }

  return { pathname, cacheEligible: false };
}

// dispatchResult turns a resolved route into a response: it serves static
// assets and dispatch misses from the Assets binding, forwards lambda routes to
// their Function URL, and rejects edge routes (not wired yet). Kept separate
// from resolveRoutes so this — the routing layer's decision logic — is unit
// testable without a manifest module or live bindings.
export async function dispatchResult(
  result: RouteResult,
  request: Request,
  deps: RouteDeps,
): Promise<Response> {
  const { manifest, functionUrls, assets } = deps;
  const doFetch = deps.fetch ?? fetch;
  const url = new URL(request.url);

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

  // An identity for lookup only: Next inside the Lambda has no '/index.rsc'
  // route, it expects '/' plus the RSC headers. Everything below forwards the
  // ORIGINAL pathname and headers.
  const rsc = resolveRsc(result.resolvedPathname, request.headers, manifest);

  const target = manifest.dispatch[rsc.pathname];
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

    case "prerender":
    case "lambda": {
      const fnUrl = functionUrls[target.id];
      if (!fnUrl) {
        return new Response(`No function URL for ${target.id}`, {
          status: 502,
        });
      }

      // Forward the (possibly rewritten) invocation target path+query. The AWS
      // Function URL takes a normal HTTP request; the lambdanode bootstrap wraps
      // it into the v2 event the Go runtime parses.
      const forwardUrl = result.invocationTarget
        ? new URL(result.invocationTarget.pathname + url.search, fnUrl)
        : new URL(url.pathname + url.search, fnUrl);

      // Built per call: a background refresh reaches the origin a second time,
      // and a Request cannot be replayed.
      const origin = () =>
        doFetch(
          new Request(forwardUrl, {
            method: request.method,
            headers: request.headers,
            body: request.body,
            redirect: "manual",
          }),
        );

      // A lambda route serves per-user data under default headers; it must never
      // become cacheable by omission.
      if (target.kind !== "prerender" || !rsc.cacheEligible || !deps.cache) {
        return origin();
      }

      return serveCached(
        request,
        {
          key: cacheKey(manifest.buildId, rsc.pathname, url, target.allowQuery),
          tags: target.tags,
        },
        deps.cache,
        origin,
      );
    }

    case "edge":
      // TODO: edge functions run in-Worker; import the compiled edge entry by
      // target.entryKey and invoke it.
      return new Response("Edge runtime not wired yet", { status: 501 });

    default:
      return assetsOr404(request, assets);
  }
}

// assetsOr404 serves a request from the Assets binding, mapping the binding's
// own 404 to a plain 404 so callers can treat a miss uniformly.
async function assetsOr404(
  request: Request,
  assets: Pick<Fetcher, "fetch">,
): Promise<Response> {
  const res = await assets.fetch(request);
  return res.status === 404
    ? new Response("Not Found", { status: 404 })
    : res;
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
      routes: manifest.routes as Parameters<
        typeof resolveRoutes
      >[0]["routes"],

      // TODO: invoke user-defined middleware
      invokeMiddleware: async () => {
        return {};
      },
    })) as RouteResult;

    return dispatchResult(result, request, {
      manifest,
      functionUrls: JSON.parse(env.FUNCTION_URLS) as Record<string, string>,
      assets: env.ASSETS,
      fetch,
      // Dormant on *.workers.dev, where cache operations are silently discarded;
      // it comes alive once the worker is routed through a custom domain.
      cache: {
        cache: caches.default,
        waitUntil: (promise) => ctx.waitUntil(promise),
      },
    });
  },
} satisfies ExportedHandler<Env>;

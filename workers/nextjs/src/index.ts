import { resolveRoutes } from "@next/routing";
import { CacheDeps, cacheKey, serveCached, withStatus } from "./cache";
import {
  intercept,
  readInterceptionConfig,
  signerFor,
  type InterceptDeps,
  type InterceptionConfig,
} from "./interception";

interface Env {
  ASSETS: Fetcher;
  FUNCTION_URLS: string;
  // Bound only where the edge provisioned a cache store. Its presence is what
  // moves the ISR read path off AWS entirely.
  OCEL_CACHE_STORE?: R2Bucket;
  OCEL_EDGE_ACCESS_KEY_ID?: string;
  OCEL_EDGE_SECRET_KEY?: string;
  OCEL_AWS_REGION?: string;
  OCEL_ISR_BUCKET?: string;
  OCEL_STATE_TABLE?: string;
  OCEL_ISR_PREFIX?: string;
  OCEL_ISR_TAG_NAMESPACE?: string;
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
        filePath: string | undefined;
        initialStatus?: number;
        initialHeaders?: Record<string, string | string[]>;
        initialExpiration?: number;
        initialRevalidate?: number | false;
        postponedState: string | undefined;
      };
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

  // Present when the deploy injected edge cache coordinates: prerender routes
  // then try reading the authoritative ISR cache directly — through the bound
  // store when there is one, over signed S3+DynamoDB when there is not — before
  // falling open to the Lambda origin. Absent leaves the Lambda path unchanged.
  interception?: Pick<InterceptDeps, "signedFetch" | "store" | "snapshotCache" | "now"> & {
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

    case "prerender":
    case "lambda": {
      const fnUrl = functionUrls[target.id];
      if (!fnUrl) {
        return new Response(`No function URL for ${target.id}`, {
          status: 502,
        });
      }

      const forwardUrl = result.invocationTarget
        ? new URL(result.invocationTarget.pathname + url.search, fnUrl)
        : new URL(url.pathname + url.search, fnUrl);

      let origin = () =>
        doFetch(
          new Request(forwardUrl, {
            method: request.method,
            headers: request.headers,
            body: request.body,
            redirect: "manual",
          }),
        );

      if (target.kind !== "prerender" || !deps.cache) {
        return origin();
      }

      let shouldBypass = false;

      for (const bypass of target.config.bypassFor ?? []) {
        if (shouldBypass) break;
        if (bypass.type === "header") {
          const h = request.headers.get(bypass.key);
          shouldBypass = bypass.value ? h === bypass.value : h !== null;
        } else if (bypass.type === "cookie") {
          const cookie = request.headers.get("cookie");
          const entry = (cookie?.split(";") ?? [])
            .map((e) => e.trim())
            .find((e) => {
              const eq = e.indexOf("=");
              return eq > 0 && e.slice(0, eq) === bypass.key;
            });
          const value = entry ? entry.slice(entry.indexOf("=") + 1) : undefined;
          shouldBypass =
            entry !== undefined &&
            (bypass.value ? value === bypass.value : true);
        } else if (bypass.type === "host") {
          shouldBypass = bypass.value === url.host;
        } else if (bypass.type === "query") {
          const q = url.searchParams.get(bypass.key);
          shouldBypass = bypass.value ? q === bypass.value : q !== null;
        }
      }

      const bypassToken = request.headers.get("x-prerender-revalidate");
      if (
        target.config.bypassToken &&
        bypassToken === target.config.bypassToken
      ) {
        shouldBypass = true;
      }

      if (shouldBypass) {
        return withStatus(await origin(), "BYPASS");
      }

      const safeHeaders = new Headers();
      const allowedHeaders = target.config.allowHeader?.map((h) =>
        h.toLowerCase(),
      );

      for (const [name, value] of request.headers) {
        if (allowedHeaders?.includes(name.toLowerCase())) {
          safeHeaders.set(name, value);
        }
      }

      origin = () =>
        doFetch(
          new Request(forwardUrl, {
            method: request.method,
            headers: safeHeaders,
            body: request.body,
            redirect: "manual",
          }),
        );

      const blockingHeaders = new Headers(safeHeaders);
      blockingHeaders.set(
        "x-prerender-revalidate",
        target.config.bypassToken ?? "",
      );

      const originBlocking = () =>
        doFetch(
          new Request(forwardUrl, {
            method: request.method,
            headers: blockingHeaders,
            body: request.body,
            redirect: "manual",
          }),
        );

      // A pages-router data request (/_next/data/<build>/route.json) resolves to
      // the same prerender target as its html route, but must be answered with
      // JSON pageData, not html. Interception reconstructs only the html/RSC
      // variants, so those requests fall open to the Lambda exactly as today.
      const isNextData =
        url.pathname.startsWith((manifest.basePath ?? "") + "/_next/data/") &&
        url.pathname.endsWith(".json");

      // When edge cache coordinates are present, a prerender read is tried
      // directly against the cache first; any miss/expiry/error falls open to
      // the Lambda origin. The interception hit carries a full-window
      // cache-control so serveCached memoizes it exactly as it would the
      // Lambda's response.
      let cachingOrigin = origin;
      if (target.kind === "prerender" && deps.interception && !isNextData) {
        const lambdaOrigin = origin;
        const interceptTarget = {
          routePath: result.invocationTarget?.pathname ?? url.pathname,
          revalidate: target.fallback?.initialRevalidate,
        };
        const { config, ...interceptDeps } = deps.interception;
        cachingOrigin = async () =>
          (await intercept(request, interceptTarget, config, interceptDeps)) ??
          lambdaOrigin();
      }

      return serveCached(
        request,
        {
          key: cacheKey(manifest.buildId, url.pathname, url, target.allowQuery),
          tags: target.tags,
        },
        deps.cache,
        cachingOrigin,
        originBlocking,
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

    const interceptionConfig = readInterceptionConfig(
      env as unknown as Record<string, string | undefined>,
    );

    return dispatchResult(result, request, {
      manifest,
      functionUrls: JSON.parse(env.FUNCTION_URLS) as Record<string, string>,
      assets: env.ASSETS,
      fetch,
      cache: {
        cache: caches.default,
        waitUntil: (promise) => ctx.waitUntil(promise),
      },
      interception: interceptionConfig
        ? {
            config: interceptionConfig,
            signedFetch: signerFor(interceptionConfig),
            // Passed as the binding itself: it is one stable object per isolate,
            // which is what the snapshot memo keys on.
            store: env.OCEL_CACHE_STORE,
            snapshotCache: caches.default,
          }
        : undefined,
    });
  },
} satisfies ExportedHandler<Env>;

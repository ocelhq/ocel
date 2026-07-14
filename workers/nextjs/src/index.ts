import { resolveRoutes } from "@next/routing";

interface Env {
  ASSETS: Fetcher;
  FUNCTION_URLS: string;
}

type DispatchTarget =
  | { kind: "static" }
  | { kind: "lambda"; id: string; parent?: string; revalidate?: unknown }
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
  // The Workers Assets binding (or any Fetcher) serving .ocel/output/static.
  assets: Pick<Fetcher, "fetch">;
  // Injectable so lambda/external forwarding can be observed in tests.
  fetch?: typeof fetch;
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

      const proxied = new Request(forwardUrl, {
        method: request.method,
        headers: request.headers,
        body: request.body,
        redirect: "manual",
      });
      return doFetch(proxied);
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
  async fetch(request, env): Promise<Response> {
    // The routing manifest is uploaded alongside the worker as a JSON module,
    // so its parsed object is the module's default export. The variable
    // specifier keeps esbuild from trying to inline a file that only exists at
    // deploy time.
    const specifier = "./routing-manifest.json";
    const manifest = (await import(specifier)).default as Manifest;

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
    });
  },
} satisfies ExportedHandler<Env>;

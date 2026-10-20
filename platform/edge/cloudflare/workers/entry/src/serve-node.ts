import { readBypass, revalidateHeader } from "@platform/edge-contract/bypass";
import { edgeCacheHeader } from "@platform/edge-contract/edge-cache";
import {
  addRedirectQuery,
  type CacheRule,
  type CompiledRouteTable,
  findHeaders,
  findRedirect,
  findRoute,
  findSlashRedirect,
} from "@platform/edge-contract/ocel-route-table";
import { purgeHeader, readPurge } from "@platform/edge-contract/purge";
import {
  encodeForwardedSearch,
  needsSlashNormalization,
  readRawPath,
} from "@platform/edge-contract/request-path";
import { buildPathTag } from "./serve-headers";
import {
  buildCanonicalUrl,
  buildOriginCall,
  buildServeRequest,
  dropBodyForHead,
  isRead,
  RENDER_RELEASE_HEADER,
  type RenderRelease,
  SERVE_LOOKUP_ONLY_HEADER,
  SERVE_MISS_HEADER,
  SERVE_REFUSED_HEADER,
} from "./serve-key";
import { buildVisitorResponse, hasDraftCookie, type Serving } from "./serve-prerender";

export interface ServeNodePorts {
  serving: Serving;
  app: string;
  origin: string;
  routes: CompiledRouteTable | undefined;
  forward: (request: Request) => Promise<Response>;
}

const CONDITIONAL_HEADERS = ["if-none-match", "if-modified-since"] as const;

function canBeCached(request: Request): boolean {
  return (
    !request.headers.has("authorization") && !needsSlashNormalization(readRawPath(request.url))
  );
}

function answerRedirect(
  routes: CompiledRouteTable,
  url: URL,
  redirect: { status: number; location: string },
): Response {
  const headers = new Headers(findHeaders(routes, url.pathname));
  headers.set("location", addRedirectQuery(redirect.location, url.search));
  return new Response(null, { status: redirect.status, headers });
}

function findEdgeRedirect(
  routes: CompiledRouteTable,
  request: Request,
  url: URL,
): { status: number; location: string } | undefined {
  const slash = isRead(request) ? findSlashRedirect(routes, url.pathname) : undefined;
  return slash ? { status: 308, location: slash } : findRedirect(routes, url.pathname);
}

export function serveNodeOrigin(ports: ServeNodePorts): (request: Request) => Promise<Response> {
  const { serving, app, origin, routes, forward } = ports;

  async function forwardUncached(request: Request): Promise<Response> {
    const response = await forward(request);
    const announced = response.headers.get(purgeHeader);
    if (announced === null) return response;
    await serving.purge(readPurge(announced), !isRead(request));
    const answered = new Response(response.body, response);
    answered.headers.delete(purgeHeader);
    return answered;
  }

  async function serveCached(
    request: Request,
    rule: CacheRule | undefined,
    onlyLooksUp: boolean,
  ): Promise<Response> {
    const url = new URL(request.url);
    const canonical = new URL(buildCanonicalUrl(url, rule?.allowQuery));
    const target = new URL(canonical.pathname + encodeForwardedSearch(canonical.search), origin);
    const call = buildOriginCall(target.toString(), {
      host: serving.host,
      app,
      release: serving.release,
    });
    const served = buildServeRequest(call.url, request);
    served.headers.set(edgeCacheHeader, "1");
    if (onlyLooksUp) {
      served.headers.set(SERVE_LOOKUP_ONLY_HEADER, "1");
      for (const name of CONDITIONAL_HEADERS) served.headers.delete(name);
    }
    const release: RenderRelease = {
      slug: serving.slug,
      storageCoordinate: serving.storageCoordinate,
      tags: [],
      ...(!rule && { honoursAppCacheHeaders: true }),
    };
    served.headers.set(RENDER_RELEASE_HEADER, JSON.stringify(release));

    let response: Response;
    try {
      response = await serving.serve({ props: call.props }).fetch(served);
    } catch (error) {
      console.error("ocel: Serve failed for a cached route, forwarding it uncached", error);
      return forwardUncached(request);
    }
    if (response.headers.has(SERVE_REFUSED_HEADER) || response.headers.has(SERVE_MISS_HEADER)) {
      await response.body?.cancel();
      return forwardUncached(request);
    }
    return dropBodyForHead(request, buildVisitorResponse(response, false));
  }

  async function revalidate(request: Request): Promise<Response> {
    const rendered = await forwardUncached(request);
    if (rendered.ok) {
      const tag = buildPathTag(serving.release, new URL(request.url).pathname);
      await serving.purge([{ mode: "purge", tags: [tag] }], true);
    }
    return rendered;
  }

  return async (request) => {
    if (!routes) return forwardUncached(request);
    const url = new URL(request.url);
    const redirect = findEdgeRedirect(routes, request, url);
    if (redirect) return answerRedirect(routes, url, redirect);

    const rule = findRoute(routes, url.pathname)?.route.cache;
    if (!rule) {
      return isRead(request) && canBeCached(request) && !hasDraftCookie(request)
        ? serveCached(request, undefined, request.headers.has("cookie"))
        : forwardUncached(request);
    }
    switch (
      readBypass({
        token: rule.bypassToken,
        method: request.method,
        cookie: request.headers.get("cookie"),
        revalidate: request.headers.get(revalidateHeader),
      })
    ) {
      case "bypass":
        return forwardUncached(request);
      case "revalidate":
        return revalidate(request);
      default:
        return isRead(request) && canBeCached(request)
          ? serveCached(request, rule, false)
          : forwardUncached(request);
    }
  };
}

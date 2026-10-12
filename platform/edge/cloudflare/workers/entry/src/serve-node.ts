import { encodeForwardedSearch } from "@framework/next-router/request-target";
import { needsSlashNormalization } from "@framework/next-router/trailing-slash";
import { readBypass, revalidateHeader } from "@platform/edge-contract/bypass";
import { edgeCacheHeader } from "@platform/edge-contract/edge-cache";
import {
  type CacheRule,
  type CompiledRouteTable,
  findHeaders,
  findRedirect,
  findRoute,
  findSlashRedirect,
} from "@platform/edge-contract/ocel-route-table";
import { purgeHeader, readPurge } from "@platform/edge-contract/purge";
import { buildPathTag } from "./serve-headers";
import {
  buildCanonicalUrl,
  buildOriginCall,
  buildServeRequest,
  RENDER_RELEASE_HEADER,
  type RenderRelease,
  SERVE_REFUSED_HEADER,
} from "./serve-key";
import { buildVisitorResponse, type Serving } from "./serve-prerender";

export interface ServeNodePorts {
  serving: Serving;
  app: string;
  origin: string;
  routes: CompiledRouteTable | undefined;
  forward: (request: Request) => Promise<Response>;
}

function isRead(request: Request): boolean {
  return request.method === "GET" || request.method === "HEAD";
}

function readRawPath(request: Request): string {
  const pathAndQuery = request.url.replace(/^[a-z][a-z\d+.-]*:\/\/[^/]*/i, "");
  const queryIndex = pathAndQuery.indexOf("?");
  return queryIndex === -1 ? pathAndQuery : pathAndQuery.slice(0, queryIndex);
}

function canBeCached(request: Request): boolean {
  return (
    request.method === "GET" &&
    !request.headers.has("authorization") &&
    !needsSlashNormalization(readRawPath(request))
  );
}

function answerRedirect(
  routes: CompiledRouteTable,
  url: URL,
  redirect: { status: number; location: string },
): Response {
  const headers = new Headers(findHeaders(routes, url.pathname));
  headers.set(
    "location",
    redirect.location.includes("?") ? redirect.location : redirect.location + url.search,
  );
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
    const response = await forward(routes ? new Request(request, { cache: "no-store" }) : request);
    const announced = response.headers.get(purgeHeader);
    if (announced === null) return response;
    await serving.purge(readPurge(announced), !isRead(request));
    const answered = new Response(response.body, response);
    answered.headers.delete(purgeHeader);
    return answered;
  }

  async function serveCached(request: Request, rule: CacheRule): Promise<Response> {
    const url = new URL(request.url);
    const canonical = new URL(buildCanonicalUrl(url, rule.allowQuery));
    const target = new URL(canonical.pathname + encodeForwardedSearch(canonical.search), origin);
    const call = buildOriginCall(target.toString(), {
      host: serving.host,
      app,
      release: serving.release,
    });
    const served = buildServeRequest(call.url, request);
    served.headers.set(edgeCacheHeader, "1");
    const release: RenderRelease = {
      slug: serving.slug,
      isrPrefix: serving.isrPrefix,
      tags: [],
    };
    served.headers.set(RENDER_RELEASE_HEADER, JSON.stringify(release));

    const response = await serving.serve({ props: call.props }).fetch(served);
    if (response.headers.has(SERVE_REFUSED_HEADER)) {
      await response.body?.cancel();
      return forwardUncached(request);
    }
    return buildVisitorResponse(response, false);
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
    if (!rule) return forwardUncached(request);
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
        return canBeCached(request) ? serveCached(request, rule) : forwardUncached(request);
    }
  };
}

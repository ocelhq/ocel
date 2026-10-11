import { encodeForwardedSearch } from "@framework/next-router/request-target";
import { needsSlashNormalization } from "@framework/next-router/trailing-slash";
import { findCacheRule } from "@platform/edge-contract/cache-rules";
import { edgeCacheHeader } from "@platform/edge-contract/edge-cache";
import type { CacheRule } from "@platform/edge-contract/hosting";
import { purgeHeader, readPurge } from "@platform/edge-contract/purge";
import {
  buildCanonicalUrl,
  buildOriginCall,
  buildServeRequest,
  RENDER_RELEASE_HEADER,
  type RenderRelease,
  SERVE_REFUSED_HEADER,
} from "./serve-key";
import { buildVisitorResponse, type Serving } from "./serve-prerender";

export interface ServeNodeDeps {
  serving: Serving;
  app: string;
  origin: string;
  rules: readonly CacheRule[];
  forward: (request: Request) => Promise<Response>;
}

function isRead(request: Request): boolean {
  return request.method === "GET" || request.method === "HEAD";
}

function rawPath(request: Request): string {
  const pathAndQuery = request.url.replace(/^[a-z][a-z\d+.-]*:\/\/[^/]*/i, "");
  const queryIndex = pathAndQuery.indexOf("?");
  return queryIndex === -1 ? pathAndQuery : pathAndQuery.slice(0, queryIndex);
}

function isCacheable(request: Request, rules: readonly CacheRule[]): boolean {
  return (
    request.method === "GET" &&
    !request.headers.has("authorization") &&
    !needsSlashNormalization(rawPath(request)) &&
    findCacheRule(rules, new URL(request.url).pathname) !== undefined
  );
}

export function serveNodeOrigin(deps: ServeNodeDeps): (request: Request) => Promise<Response> {
  const { serving, app, origin, rules, forward } = deps;

  async function forwardUncached(request: Request): Promise<Response> {
    const response = await forward(request);
    const announced = response.headers.get(purgeHeader);
    if (announced === null) return response;
    await serving.purge(readPurge(announced), !isRead(request));
    const answered = new Response(response.body, response);
    answered.headers.delete(purgeHeader);
    return answered;
  }

  async function serveCached(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const canonical = new URL(buildCanonicalUrl(url));
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

  return (request) =>
    isCacheable(request, rules) ? serveCached(request) : forwardUncached(request);
}

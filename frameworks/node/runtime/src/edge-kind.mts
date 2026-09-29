export const routingManifestPathVar = "OCEL_ROUTING_MANIFEST";

export { dispatchesAtOrigin, invalidatesByCacheTag } from "./host.mjs";

export const routerHeader = "x-ocel-router";

export function withRouterHeader(response: Response, routerKind: string): Response {
  if (!routerKind || response.headers.get(routerHeader) === routerKind) return response;
  const marked = new Response(response.body, response);
  marked.headers.set(routerHeader, routerKind);
  return marked;
}

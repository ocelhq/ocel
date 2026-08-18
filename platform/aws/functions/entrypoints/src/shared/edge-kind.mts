export const routingManifestPathVar = "OCEL_ROUTING_MANIFEST";

export { invalidatesByCacheTag, routerMode } from "./membrane.mjs";

export const edgeHeader = "x-ocel-edge";

export function withEdgeHeader(response: Response, edgeKind: string): Response {
  if (!edgeKind || response.headers.get(edgeHeader) === edgeKind) return response;
  const marked = new Response(response.body, response);
  marked.headers.set(edgeHeader, edgeKind);
  return marked;
}

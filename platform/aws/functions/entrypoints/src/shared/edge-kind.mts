const cloudflareEdgeKind = "cloudflare";

export const routingManifestPathVar = "OCEL_ROUTING_MANIFEST";

export function routerMode(edgeKind: string | undefined): boolean {
  return (
    edgeKind !== undefined && edgeKind !== "" && edgeKind !== cloudflareEdgeKind
  );
}

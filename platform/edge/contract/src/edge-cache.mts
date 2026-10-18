export const releaseTokenVar = "OCEL_RELEASE_TOKEN";

export const edgeCacheVar = "OCEL_EDGE_CACHE";

export const edgeCacheControlHeaderVar = "OCEL_EDGE_CACHE_CONTROL_HEADER";

export function readEdgeCacheControlHeader(env: NodeJS.ProcessEnv): string | undefined {
  return env[edgeCacheControlHeaderVar] || undefined;
}

export type EdgeCacheMode = "marked" | "all";

export function readEdgeCacheMode(env: NodeJS.ProcessEnv): EdgeCacheMode | undefined {
  const mode = env[edgeCacheVar];
  if (mode === undefined || mode === "") return undefined;
  if (mode === "marked" || mode === "all") return mode;
  throw new Error(`ocel: ${edgeCacheVar} is ${mode}, which is neither marked nor all`);
}

export function cachesOnlyMarked(mode: EdgeCacheMode | undefined): boolean {
  return mode === "marked";
}

export const edgeCacheHeader = "x-ocel-edge-cache";

export const edgeCacheTagHeader = "x-ocel-cache-tag";

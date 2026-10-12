export const releaseTokenVar = "OCEL_RELEASE_TOKEN";

export const edgeCacheVar = "OCEL_EDGE_CACHE";

export type EdgeCacheMode = "none" | "marked" | "all";

export function readEdgeCacheMode(env: NodeJS.ProcessEnv): EdgeCacheMode {
  const mode = env[edgeCacheVar];
  if (mode === undefined || mode === "") return "none";
  if (mode === "marked" || mode === "all") return mode;
  throw new Error(`ocel: ${edgeCacheVar} is ${mode}, which is neither marked nor all`);
}

export function servesOnlyFresh(env: NodeJS.ProcessEnv): boolean {
  return readEdgeCacheMode(env) !== "none";
}

export const edgeCacheHeader = "x-ocel-edge-cache";

export const edgeCacheTagHeader = "x-ocel-cache-tag";

export const releaseTokenVar = "OCEL_RELEASE_TOKEN";

const RELEASE_TOKEN = /^r[0-9a-f]{8}$/;

export function readReleaseToken(env: NodeJS.ProcessEnv): string | undefined {
  const release = env[releaseTokenVar];
  if (!release) return undefined;
  if (!RELEASE_TOKEN.test(release)) {
    throw new Error(`ocel: ${releaseTokenVar} is ${release}, which is not a release token`);
  }
  return release;
}

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

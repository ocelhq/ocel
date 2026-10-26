import { invalidatesByCacheTag } from "@framework/node-runtime/host";
import { readReleaseToken } from "@platform/edge-contract/edge-cache";

export const cloudCdnTagLimits = { perObject: 50, bytesPerTag: 120, bytesPerObject: 4096 } as const;

export const uncacheable = "private, no-cache, no-store, max-age=0, must-revalidate";

function isImmutable(cacheControl: string | null): boolean {
  return (cacheControl ?? "")
    .split(",")
    .some((directive) => directive.trim().toLowerCase() === "immutable");
}

export function cacheTagsForCloudCdn(
  release: string | null,
  header: string | null,
  cacheControl: string | null,
): { value: string | null; dropped: string[] } {
  const own = new Set<string>();
  for (const token of (header ?? "").split(",")) {
    const tag = token.trim();
    if (tag !== "") own.add(tag);
  }
  const tagged = release !== null && !isImmutable(cacheControl);
  if (tagged) own.delete(release);
  const kept: string[] = [];
  const dropped: string[] = [];
  let bytes = 0;
  for (const tag of tagged ? [release, ...own] : own) {
    const joined = bytes + Buffer.byteLength(tag) + (kept.length > 0 ? 1 : 0);
    if (
      kept.length < cloudCdnTagLimits.perObject &&
      Buffer.byteLength(tag) <= cloudCdnTagLimits.bytesPerTag &&
      joined <= cloudCdnTagLimits.bytesPerObject
    ) {
      kept.push(tag);
      bytes = joined;
    } else {
      dropped.push(tag);
    }
  }
  return { value: kept.length > 0 ? kept.join(",") : null, dropped };
}

export function cloudCdnRelease(env: NodeJS.ProcessEnv): string | null {
  if (!invalidatesByCacheTag(env)) return null;
  return readReleaseToken(env) ?? null;
}

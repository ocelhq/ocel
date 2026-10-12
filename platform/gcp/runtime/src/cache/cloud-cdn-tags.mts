import { invalidatesByCacheTag } from "@framework/node-runtime/host";
import { isImmutableCacheControl } from "@framework/node-runtime/origin-cache-control";
import { readReleaseToken } from "@platform/edge-contract/purge";

const releasePattern = /^r[0-9a-f]{8}$/;

export const cloudCdnTagLimits = { perObject: 50, bytesPerTag: 120, bytesPerObject: 4096 } as const;

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
  const tagged = release !== null && !isImmutableCacheControl(cacheControl);
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
  const prefix = env.OCEL_ISR_PREFIX;
  if (!invalidatesByCacheTag(env) || !prefix) return null;
  const release = readReleaseToken(prefix);
  return release !== undefined && releasePattern.test(release) ? release : null;
}

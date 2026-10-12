import { invalidatesByCacheTag } from "@framework/node-runtime/host";
import { readReleaseToken } from "@platform/edge-contract/purge";

const releasePattern = /^r[0-9a-f]{8}$/;

export const cloudCdnTagLimits = { perObject: 50, bytesPerTag: 120, bytesPerObject: 4096 } as const;

export function cloudCdnRelease(env: NodeJS.ProcessEnv): string | null {
  const prefix = env.OCEL_ISR_PREFIX;
  if (!invalidatesByCacheTag(env) || !prefix) return null;
  const release = readReleaseToken(prefix);
  return release !== undefined && releasePattern.test(release) ? release : null;
}

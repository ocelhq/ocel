import { readStorageCoordinate } from "@platform/edge-contract/storage-key";
import { newCdnPurge, type PurgeTags } from "./cdn-purge.mjs";
import { cloudCdnRelease } from "./cloud-cdn-tags.mjs";
import { newCloudflarePurge } from "./cloudflare-purge.mjs";

export function openPurgeFor(env: NodeJS.ProcessEnv): PurgeTags | undefined {
  const urlMap = env.OCEL_CDN_URL_MAP;
  const release = cloudCdnRelease(env);
  if (urlMap && release === null) {
    throw new Error(
      "ocel: OCEL_CDN_URL_MAP names a url map to purge, and this service is told no release its responses are tagged with",
    );
  }
  if (urlMap && release !== null) return newCdnPurge({ urlMap, release });
  const url = env.OCEL_PURGE_URL;
  if (!url) return undefined;
  const secret = env.OCEL_PURGE_SECRET;
  const coordinate = readStorageCoordinate(env);
  if (release === null || coordinate === undefined || !secret) {
    throw new Error(
      "ocel: OCEL_PURGE_URL names where to purge, and this service is told no release its responses are tagged with or no OCEL_PURGE_SECRET to sign the purge with",
    );
  }
  return newCloudflarePurge({ url, coordinate, secret });
}

import type { AssetBucket, AssetObject } from "@framework/next-router/assets";
import type { ResponseCache } from "@framework/next-router/http-cache";
import { retainOwner } from "@framework/next-router/origin-response";

const ABSENT = new Set([403, 404]);

export function s3AssetBucket(
  bucket: string,
  region: string,
  doFetch: typeof fetch,
): AssetBucket {
  const origin = `https://${bucket}.s3.${region}.amazonaws.com`;
  return {
    async get(key: string): Promise<AssetObject | null> {
      const response = retainOwner(await doFetch(`${origin}/${objectPath(key)}`));
      if (!response.ok || !response.body) {
        await response.body?.cancel();
        if (response.ok || ABSENT.has(response.status)) return null;
        throw new Error(
          `ocel: reading ${key} out of ${bucket} answered ${response.status}`,
        );
      }
      const etag = response.headers.get("etag");
      return { body: response.body, ...(etag ? { httpEtag: etag } : {}) };
    },
  };
}

function objectPath(key: string): string {
  return key
    .split("/")
    .map((segment) => encodeURIComponent(segment))
    .join("/");
}

export function uncachedResponses(): ResponseCache {
  return {
    match: async () => undefined,
    put: async (_request, response) => {
      await response.body?.cancel();
    },
  };
}

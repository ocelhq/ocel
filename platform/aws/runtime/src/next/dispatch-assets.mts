import type { AssetBucket, AssetObject } from "@framework/next-router/assets";
import { retainOwner } from "@framework/next-router/origin-response";

const ABSENT = new Set([403, 404]);

const ERROR_BODY_DEADLINE_MS = 1000;

export function s3AssetBucket(bucket: string, region: string, doFetch: typeof fetch): AssetBucket {
  const origin = `https://${bucket}.s3.${region}.amazonaws.com`;
  return {
    async get(key: string): Promise<AssetObject | null> {
      const request = new AbortController();
      const response = retainOwner(
        await doFetch(`${origin}/${objectPath(key)}`, { signal: request.signal }),
      );
      if (!response.ok || !response.body) {
        const deadline = setTimeout(() => request.abort(), ERROR_BODY_DEADLINE_MS);
        await response.arrayBuffer().catch(() => undefined);
        clearTimeout(deadline);
        if (response.ok || ABSENT.has(response.status)) return null;
        throw new Error(`ocel: reading ${key} out of ${bucket} answered ${response.status}`);
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

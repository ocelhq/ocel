import type http from "node:http";
import { beforeWriteHead } from "@framework/node-runtime/write-head";
import { cacheTagsForCloudCdn, uncacheable } from "../cache/cloud-cdn-tags.mjs";

export function withholdOverTaggedResponsesFromCloudCdn(prototype: http.ServerResponse): void {
  beforeWriteHead(prototype, (res) => {
    const tags = res.getHeader("cache-tag");
    if (tags === undefined) return;
    const { dropped } = cacheTagsForCloudCdn(null, String(tags), null);
    if (dropped.length === 0) return;
    console.warn(
      `ocel: ${res.req?.url ?? "a response"} carries ${dropped.length} cache tags past Cloud CDN's limits, so Cloud CDN does not store it and revalidating ${dropped.join(", ")} cannot miss it`,
    );
    res.setHeader("cache-control", uncacheable);
    res.removeHeader("cache-tag");
  });
}

import type http from "node:http";
import { cacheTagsForCloudCdn, uncacheable } from "../cache/cloud-cdn-tags.mjs";

export function withholdOverTaggedResponsesFromCloudCdn(prototype: http.ServerResponse): void {
  const writeHead = prototype.writeHead;
  prototype.writeHead = function (this: http.ServerResponse, ...args: any[]) {
    const tags = this.headersSent ? undefined : this.getHeader("cache-tag");
    if (tags !== undefined) {
      const { dropped } = cacheTagsForCloudCdn(null, String(tags), null);
      if (dropped.length > 0) {
        console.warn(
          `ocel: ${this.req?.url ?? "a response"} carries ${dropped.length} cache tags past Cloud CDN's limits, so Cloud CDN does not store it and revalidating ${dropped.join(", ")} cannot miss it`,
        );
        this.setHeader("cache-control", uncacheable);
        this.removeHeader("cache-tag");
      }
    }
    return (writeHead as any).apply(this, args);
  } as typeof prototype.writeHead;
}

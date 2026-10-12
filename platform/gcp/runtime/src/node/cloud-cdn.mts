import type http from "node:http";
import { uncacheableCacheControl } from "@framework/node-runtime/origin-cache-control";
import {
  newWriteHeadLookup,
  readWriteHeadHeaders,
  withoutWriteHeadHeaders,
} from "@framework/node-runtime/write-head";
import { cacheTagsForCloudCdn } from "../cache/cloud-cdn-tags.mjs";

export function withholdOverTaggedResponsesFromCloudCdn(prototype: http.ServerResponse): void {
  const writeHead = prototype.writeHead;
  prototype.writeHead = function (this: http.ServerResponse, ...args: any[]) {
    if (!this.headersSent) {
      const { at, passed } = readWriteHeadHeaders(args);
      const tags = newWriteHeadLookup(this, passed).get("cache-tag");
      if (tags !== null) {
        const { dropped } = cacheTagsForCloudCdn(null, tags, null);
        if (dropped.length > 0) {
          console.warn(
            `ocel: ${this.req?.url ?? "a response"} carries ${dropped.length} cache tags past Cloud CDN's limits, so Cloud CDN does not store it and revalidating ${dropped.join(", ")} cannot miss it`,
          );
          this.setHeader("cache-control", uncacheableCacheControl);
          this.removeHeader("cache-tag");
          if (passed !== undefined) {
            args[at] = withoutWriteHeadHeaders(passed, ["cache-control", "cache-tag"]);
          }
        }
      }
    }
    return (writeHead as any).apply(this, args);
  } as typeof prototype.writeHead;
}

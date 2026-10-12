import type http from "node:http";
import {
  isImmutableCacheControl,
  uncacheableCacheControl,
} from "@framework/node-runtime/origin-cache-control";
import {
  newWriteHeadLookup,
  readWriteHeadHeaders,
  withoutWriteHeadHeaders,
} from "@framework/node-runtime/write-head";
import { type HeaderLookup, isStoredBySomeEdge } from "@platform/edge-contract/cache-control";

export function isWithheldFromSharedCache(headers: HeaderLookup, now: number): boolean {
  return !isImmutableCacheControl(headers.get("cache-control")) && isStoredBySomeEdge(headers, now);
}

export function withholdResponsesFromSharedCache(prototype: http.ServerResponse): void {
  const writeHead = prototype.writeHead;
  prototype.writeHead = function (this: http.ServerResponse, ...args: any[]) {
    if (!this.headersSent) {
      const { at, passed } = readWriteHeadHeaders(args);
      if (isWithheldFromSharedCache(newWriteHeadLookup(this, passed), Date.now())) {
        this.setHeader("cache-control", uncacheableCacheControl);
        if (passed !== undefined) args[at] = withoutWriteHeadHeaders(passed, ["cache-control"]);
      }
    }
    return (writeHead as any).apply(this, args);
  } as typeof prototype.writeHead;
}

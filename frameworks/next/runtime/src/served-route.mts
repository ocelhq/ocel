import type { RevalidateWindow } from "@framework/next-protocol/launcher";
import type { RequestHeaders } from "./request-headers.mjs";

const servedRouteKey = Symbol.for("ocel.next.served-route");

const uncachedRenderKey = Symbol.for("ocel.next.uncached-render");

export function noteUncachedRender(headers: RequestHeaders): void {
  headers[uncachedRenderKey] = true;
}

export function isUncachedRender(headers: RequestHeaders): boolean {
  return headers[uncachedRenderKey] === true;
}

export function noteServedRoute(headers: RequestHeaders, window: RevalidateWindow): void {
  headers[servedRouteKey] = window;
}

export function readServedRoute(headers: RequestHeaders): RevalidateWindow | undefined {
  return headers[servedRouteKey];
}

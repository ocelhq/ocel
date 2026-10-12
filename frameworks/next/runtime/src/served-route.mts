import type { Window } from "./cache-shaping.mjs";
import type { RequestHeaders } from "./request-headers.mjs";

const servedRouteKey = Symbol.for("ocel.next.served-route");

const uncachedRenderKey = Symbol.for("ocel.next.uncached-render");

export function noteUncachedRender(headers: RequestHeaders): void {
  headers[uncachedRenderKey] = true;
}

export function isUncachedRender(headers: RequestHeaders): boolean {
  return headers[uncachedRenderKey] === true;
}

export function noteServedRoute(headers: RequestHeaders, window: Window): void {
  headers[servedRouteKey] = window;
}

export function readServedRoute(headers: RequestHeaders): Window | undefined {
  return headers[servedRouteKey];
}

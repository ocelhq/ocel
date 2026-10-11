import type { Window } from "./cache-shaping.mjs";
import type { RequestHeaders } from "./request-headers.mjs";

const servedRouteKey = Symbol.for("ocel.next.served-route");

export function noteServedRoute(headers: RequestHeaders, window: Window): void {
  headers[servedRouteKey] = window;
}

export function readServedRoute(headers: RequestHeaders): Window | undefined {
  return headers[servedRouteKey];
}

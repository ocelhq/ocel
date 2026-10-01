import type { Context } from "hono";
import { createWebRealtimeHandler, type RealtimeHandlerOptions } from "./handler.js";
import type { Realtime } from "./realtime.js";

export type { RealtimeHandlerOptions } from "./handler.js";
export * from "./index.js";

/** A Hono handler serving a realtime resource. */
export type HonoRealtimeHandler = (c: Context) => Promise<Response>;

/**
 * Serves `rt` to browsers from a Hono route, mounted at any path:
 *
 * ```ts
 * app.all("/api/realtime", createRealtimeHandler(rt));
 * ```
 *
 * It takes `POST` with `application/json` alone, serves its own origin and those in
 * `allowedOrigins`, answers `Cache-Control: no-store`, and never sets a cookie.
 * `authorize` receives the request as a Web `Request`.
 */
export function createRealtimeHandler(
  rt: Realtime<any, any>,
  options?: RealtimeHandlerOptions,
): HonoRealtimeHandler {
  const handler = createWebRealtimeHandler(rt, options);
  return (c) => {
    const request = c.req.raw;
    if (request.method === "POST") return handler.POST(request);
    if (request.method === "OPTIONS") return handler.OPTIONS(request);
    return handler.GET(request);
  };
}

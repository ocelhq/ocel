import type { NextRequest } from "next/server";
import { createWebRealtimeHandler, type RealtimeHandlerOptions } from "./handler.js";
import type { Realtime } from "./realtime.js";

export type { RealtimeHandlerOptions } from "./handler.js";
export * from "./index.js";

/** The route handlers a Next.js `route.ts` exports to serve a realtime resource. */
export interface NextRealtimeHandlers {
  /** Serves a batch of `connect`, `subscribe` and `publish` requests. */
  POST: (request: NextRequest | Request) => Promise<Response>;
  /** Refuses with 405: the handler takes POST alone. */
  GET: (request: NextRequest | Request) => Promise<Response>;
  /** Answers the CORS preflight of an origin in `allowedOrigins`, and refuses any other. */
  OPTIONS: (request: NextRequest | Request) => Promise<Response>;
}

/**
 * Serves `rt` to browsers from a Next.js route handler, mounted at any path:
 *
 * ```ts
 * // app/api/realtime/route.ts
 * export const { GET, POST, OPTIONS } = createRealtimeHandler(rt);
 * ```
 *
 * It takes `POST` with `application/json` alone, serves its own origin and those in
 * `allowedOrigins`, answers `Cache-Control: no-store`, and never sets a cookie.
 */
export function createRealtimeHandler(
  rt: Realtime<any, any>,
  options?: RealtimeHandlerOptions,
): NextRealtimeHandlers {
  return createWebRealtimeHandler(rt, options);
}

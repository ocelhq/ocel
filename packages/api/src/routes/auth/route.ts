import { auth } from "@repo/auth";

// Framework-agnostic counterpart to apps/web's `@repo/auth/next` route (which
// needs the Next-specific instance for nextCookies()). Consumed by the local
// dev API server, which is a plain Bun harness, not Next.
export function authHandler(request: Request): Promise<Response> {
  return auth.handler(request);
}

import { auth } from "@repo/auth";

// better-auth's `.handler` already returns Set-Cookie headers on the
// Response object for HTTP requests - `nextCookies()` only forwards cookies
// for direct `.api.*` calls made from Server Components/Actions, so the
// framework-agnostic `auth` instance (not `@repo/auth/next`) is enough here.
export const authHandler: (request: Request) => Promise<Response> =
  auth.handler;

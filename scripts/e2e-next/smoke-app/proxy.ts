import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";

// Node middleware (proxy.ts always builds as node middleware — there is no
// edge option for it) exercising the paths the worker has to get right: a
// rewrite, a redirect, a direct response, a request-header override, and a
// response cookie. Each lives at its own path so assert-proxy.mjs can hit it
// independently; everything else falls through to NextResponse.next() with
// the header/cookie touch applied.
export function proxy(request: NextRequest) {
  const { pathname } = request.nextUrl;

  if (pathname === "/mw/rewrite") {
    return NextResponse.rewrite(new URL("/", request.url));
  }

  if (pathname === "/mw/redirect") {
    return NextResponse.redirect(new URL("/", request.url));
  }

  if (pathname === "/mw/blocked") {
    return new NextResponse("blocked by proxy.ts", { status: 403 });
  }

  const headers = new Headers(request.headers);
  headers.set("x-ocel-proxy", "1");
  const response = NextResponse.next({ request: { headers } });
  response.cookies.set("ocel-proxy-seen", "1");
  return response;
}

export const config = {
  // Runs on every page except the build's own static output — matching it
  // too would mean the matcher covers the app's whole cached surface, which
  // is exactly the shape the adapter's blast-radius warning exists to catch.
  matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"],
};

import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";

const edgeCachedRoutes = new Set(["/isr", "/golden"]);

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
  if (!edgeCachedRoutes.has(pathname)) {
    response.cookies.set("ocel-proxy-seen", "1");
  }
  return response;
}

export const config = {
  matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"],
};

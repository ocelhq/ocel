import { type NextRequest, NextResponse } from "next/server";

export const config = { matcher: "/mw/:path*" };

export default function proxy(request: NextRequest) {
  const { pathname } = request.nextUrl;

  if (pathname === "/mw/rewrite") {
    return NextResponse.rewrite(new URL("/mw/target", request.url));
  }

  if (pathname === "/mw/redirect") {
    return NextResponse.redirect(new URL("/mw/landing", request.url));
  }

  if (pathname === "/mw/block") {
    return new NextResponse("blocked by the proxy", {
      status: 403,
      headers: { "content-type": "text/plain; charset=utf-8" },
    });
  }

  if (pathname === "/mw/inject") {
    const forwarded = new Headers(request.headers);
    forwarded.set("x-ocel-injected", "from-the-proxy");
    return NextResponse.next({ request: { headers: forwarded } });
  }

  const response = NextResponse.next();
  response.cookies.set("ocel-proxy", "fell-through", { path: "/" });
  return response;
}

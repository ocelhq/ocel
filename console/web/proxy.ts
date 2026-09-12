import { type NextRequest, NextResponse } from "next/server";
import { REQUEST_PATH_HEADER } from "@/lib/request-path";

export function proxy(request: NextRequest) {
  const requestHeaders = new Headers(request.headers);
  requestHeaders.set(REQUEST_PATH_HEADER, request.nextUrl.pathname + request.nextUrl.search);
  return NextResponse.next({ request: { headers: requestHeaders } });
}

export const config = {
  matcher: ["/((?!api|_next/static|_next/image|favicon.ico).*)"],
};

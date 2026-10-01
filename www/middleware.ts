import { type NextRequest, NextResponse } from "next/server";

const REPO = "https://github.com/ocelhq/ocel";

const GO_IMPORT = `<!doctype html>
<html>
<head>
<meta name="go-import" content="ocel.dev git ${REPO} sdk">
<meta name="go-source" content="ocel.dev ${REPO} ${REPO}/tree/main/sdk{/dir} ${REPO}/blob/main/sdk{/dir}/{file}#L{line}">
</head>
</html>
`;

export function middleware(request: NextRequest): NextResponse {
  if (request.nextUrl.searchParams.get("go-get") === "1") {
    return new NextResponse(GO_IMPORT, {
      headers: { "content-type": "text/html; charset=utf-8" },
    });
  }
  return NextResponse.redirect(new URL("/docs", request.url));
}

export const config = {
  matcher: [{ source: "/:path*", has: [{ type: "query", key: "go-get", value: "1" }] }, "/"],
};

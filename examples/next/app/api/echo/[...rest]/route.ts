export async function POST(request: Request) {
  const url = new URL(request.url);
  return Response.json({
    framework: "next",
    method: request.method,
    path: url.pathname,
    query: Object.fromEntries(url.searchParams),
    probeHeader: request.headers.get("x-ocel-probe"),
    body: await request.json(),
  });
}

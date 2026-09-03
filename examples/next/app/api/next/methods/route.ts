export const runtime = "nodejs";
export const dynamic = "force-dynamic";

const BODYLESS = new Set(["GET", "HEAD", "OPTIONS"]);

async function echo(request: Request) {
  const body = BODYLESS.has(request.method) ? "" : await request.text();
  return new Response(`${request.method}:${body}`, {
    headers: {
      "content-type": "text/plain; charset=utf-8",
      "x-ocel-method": request.method,
    },
  });
}

export const GET = echo;
export const HEAD = echo;
export const OPTIONS = echo;
export const POST = echo;
export const PUT = echo;
export const PATCH = echo;
export const DELETE = echo;

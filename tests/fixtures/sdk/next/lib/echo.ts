const BODYLESS = new Set(["GET", "HEAD", "OPTIONS"]);

export function echo<Context>(fields: (context: Context) => Promise<string[]> | string[]) {
  return async (request: Request, context: Context) => {
    const body = BODYLESS.has(request.method) ? "" : await request.text();
    return new Response([request.method, ...(await fields(context)), body].join(":"), {
      headers: {
        "content-type": "text/plain; charset=utf-8",
        "x-ocel-method": request.method,
      },
    });
  };
}

import type http from "node:http";
import type { Invoke } from "../shared/membrane.mjs";

export type FetchHandler = (request: Request) => Response | Promise<Response>;

// requestURL is the origin the app sees. The hop from the Go bootstrap to this
// process is always plain http on loopback, so only the Host header survives
// intact (the bootstrap stamps the public authority onto it); the scheme the
// client used survives only in x-forwarded-proto. Derive from both, or every
// absolute URL the app builds off request.url — a Location, a canonical tag —
// names an origin that does not exist.
export function requestURL(req: http.IncomingMessage): string {
  const forwarded = String(req.headers["x-forwarded-proto"] ?? "").split(",")[0]?.trim();
  return `${forwarded || "http"}://${req.headers.host || "localhost"}${req.url}`;
}

export function fetchToNodeHandler(fetchFn: FetchHandler): Invoke {
  return async (req, res) => {
    const body = req.method === "GET" || req.method === "HEAD" ? null : req;
    const request = new Request(requestURL(req), {
      method: req.method,
      headers: req.headers as any,
      body: body as any,
      duplex: "half",
    } as RequestInit);
    const response = await fetchFn(request);
    res.writeHead(response.status, Object.fromEntries(response.headers));
    if (response.body) {
      const reader = response.body.getReader();
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        res.write(value);
      }
    }
    res.end();
  };
}

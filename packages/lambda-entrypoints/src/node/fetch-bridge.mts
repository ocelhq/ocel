import type http from "node:http";
import type { Invoke } from "../shared/membrane.mjs";

export type FetchHandler = (request: Request) => Response | Promise<Response>;

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

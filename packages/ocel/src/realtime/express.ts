import type {
  Request as ExpressRequest,
  Response as ExpressResponse,
  RequestHandler,
} from "express";
import {
  createWebRealtimeHandler,
  maxRequestBytes,
  type RealtimeHandlerOptions,
} from "./handler.js";
import type { Realtime } from "./realtime.js";

export type { RealtimeHandlerOptions } from "./handler.js";
export * from "./index.js";

function readBoundedStream(req: ExpressRequest): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    let bytes = 0;
    const finish = (error?: Error) => {
      req.off("data", onData);
      req.off("end", onEnd);
      req.off("error", onError);
      if (error) reject(error);
      else resolve(Buffer.concat(chunks));
    };
    const onData = (chunk: Buffer) => {
      chunks.push(chunk);
      bytes += chunk.byteLength;
      if (bytes > maxRequestBytes) finish();
    };
    const onEnd = () => finish();
    const onError = (error: Error) => finish(error);
    req.on("data", onData);
    req.on("end", onEnd);
    req.on("error", onError);
  });
}

async function readExpressBody(req: ExpressRequest): Promise<Buffer | string | undefined> {
  if (req.method === "GET" || req.method === "HEAD") return undefined;
  const parsed = req.body as unknown;
  if (typeof parsed === "string" || Buffer.isBuffer(parsed)) return parsed;
  if (req.readableEnded) return parsed === undefined ? undefined : JSON.stringify(parsed);
  return readBoundedStream(req);
}

async function convertToWebRequest(req: ExpressRequest): Promise<Request> {
  const headers = new Headers();
  for (const [name, value] of Object.entries(req.headers)) {
    if (value === undefined) continue;
    for (const one of Array.isArray(value) ? value : [value]) headers.append(name, one);
  }
  const url = `${req.protocol}://${req.get("host") ?? "localhost"}${req.originalUrl}`;
  return new Request(url, { method: req.method, headers, body: await readExpressBody(req) });
}

async function send(res: ExpressResponse, response: Response): Promise<void> {
  res.status(response.status);
  response.headers.forEach((value, name) => {
    res.setHeader(name, value);
  });
  res.end(Buffer.from(await response.arrayBuffer()));
}

/**
 * Serves `rt` to browsers from an Express route, mounted at any path:
 *
 * ```ts
 * app.all("/api/realtime", createRealtimeHandler(rt));
 * ```
 *
 * It takes `POST` with `application/json` alone, serves its own origin and those in
 * `allowedOrigins`, answers `Cache-Control: no-store`, and never sets a cookie.
 * `authorize` receives the request as a Web `Request`, read whether or not a body
 * parser ran first.
 */
export function createRealtimeHandler(
  rt: Realtime<any, any>,
  options?: RealtimeHandlerOptions,
): RequestHandler {
  const handler = createWebRealtimeHandler(rt, options);
  return (req, res, next) => {
    convertToWebRequest(req)
      .then((request) => {
        if (request.method === "POST") return handler.POST(request);
        if (request.method === "OPTIONS") return handler.OPTIONS(request);
        return handler.GET(request);
      })
      .then((response) => send(res, response))
      .catch(next);
  };
}

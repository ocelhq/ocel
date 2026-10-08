import http from "node:http";
import https from "node:https";
import { Readable } from "node:stream";
import type { ReadableStream as NodeReadableStream } from "node:stream/web";

const NULL_BODY_STATUSES = new Set([101, 103, 204, 205, 304]);

function incomingHeaders(res: http.IncomingMessage): Headers {
  const headers = new Headers();
  for (let i = 0; i < res.rawHeaders.length; i += 2) {
    headers.append(res.rawHeaders[i]!, res.rawHeaders[i + 1]!);
  }
  return headers;
}

export const fetchUndecoded = ((input: Request | string | URL, init?: RequestInit) => {
  const request = new Request(input, init);
  const { signal } = request;
  if (signal.aborted) return Promise.reject(signal.reason);

  const url = new URL(request.url);
  const send = url.protocol === "https:" ? https.request : http.request;
  const headers = Object.fromEntries(request.headers);
  delete headers.host;

  return new Promise<Response>((resolve, reject) => {
    const outgoing = send(url, {
      method: request.method,
      headers,
    });
    const abort = () => {
      outgoing.destroy(signal.reason);
      reject(signal.reason);
    };
    signal.addEventListener("abort", abort, { once: true });

    outgoing.on("error", (err) => {
      signal.removeEventListener("abort", abort);
      reject(err);
    });
    outgoing.on("response", (res) => {
      res.on("close", () => signal.removeEventListener("abort", abort));
      const status = res.statusCode ?? 502;
      const hasBody = request.method !== "HEAD" && !NULL_BODY_STATUSES.has(status);
      if (!hasBody) res.resume();
      resolve(
        new Response(hasBody ? (Readable.toWeb(res) as ReadableStream<Uint8Array>) : null, {
          status,
          statusText: res.statusMessage,
          headers: incomingHeaders(res),
        }),
      );
    });

    if (!request.body) {
      outgoing.end();
      return;
    }
    Readable.fromWeb(request.body as NodeReadableStream<Uint8Array>)
      .on("error", (err) => outgoing.destroy(err))
      .pipe(outgoing);
  });
}) as typeof fetch;

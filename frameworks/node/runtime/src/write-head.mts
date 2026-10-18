import type http from "node:http";

type WriteHead = (this: http.ServerResponse, ...args: unknown[]) => http.ServerResponse;

export function beforeWriteHead(
  target: http.ServerResponse,
  hook: (res: http.ServerResponse) => void,
): void {
  const writeHead = target.writeHead as unknown as WriteHead;
  target.writeHead = function (this: http.ServerResponse, ...args: unknown[]) {
    if (this.headersSent) return writeHead.apply(this, args);
    const [status, ...rest] = args;
    const message = typeof rest[0] === "string" ? [rest.shift()] : [];
    if (typeof status === "number") this.statusCode = status;
    setPassedHeaders(this, rest[0]);
    hook(this);
    return writeHead.apply(this, [this.statusCode, ...message]);
  } as typeof target.writeHead;
}

function setPassedHeaders(res: http.ServerResponse, headers: unknown): void {
  if (Array.isArray(headers) && Array.isArray(headers[0])) {
    for (const [name, value] of headers as [string, string][]) res.appendHeader(name, value);
    return;
  }
  if (Array.isArray(headers)) {
    for (let i = 0; i + 1 < headers.length; i += 2) res.removeHeader(String(headers[i]));
    for (let i = 0; i + 1 < headers.length; i += 2) {
      res.appendHeader(String(headers[i]), headers[i + 1] as string | string[]);
    }
    return;
  }
  if (headers === null || typeof headers !== "object") return;
  for (const [name, value] of Object.entries(headers)) {
    if (value !== undefined) res.setHeader(name, value as string | string[]);
  }
}

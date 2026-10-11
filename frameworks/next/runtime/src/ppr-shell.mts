import type http from "node:http";
import {
  edgeCacheHeader,
  postponedLengthHeader,
  pprShellHeader,
} from "@platform/edge-contract/edge-cache";

interface CachedPage {
  value?: { postponed?: unknown } | null;
}

export interface PprShellMeta {
  minimalMode: true;
  onCacheEntry(entry: CachedPage): Promise<boolean>;
}

export function isPprShellRequest(req: http.IncomingMessage): boolean {
  return (
    req.method === "GET" &&
    req.headers[pprShellHeader] === "1" &&
    req.headers[edgeCacheHeader] === "1"
  );
}

function dropContentLength(headers: unknown): unknown {
  if (Array.isArray(headers)) {
    const kept: unknown[] = [];
    for (let i = 0; i < headers.length; i += 2) {
      if (String(headers[i]).toLowerCase() !== "content-length")
        kept.push(headers[i], headers[i + 1]);
    }
    return kept;
  }
  if (headers && typeof headers === "object") {
    return Object.fromEntries(
      Object.entries(headers).filter(([name]) => name.toLowerCase() !== "content-length"),
    );
  }
  return headers;
}

export function framePprShell(res: http.ServerResponse): PprShellMeta {
  let postponed: Buffer | undefined;
  let framed = false;

  const stampHeaders = () => {
    if (!postponed || res.headersSent) return;
    res.removeHeader("content-length");
    res.setHeader(postponedLengthHeader, String(postponed.byteLength));
  };
  const takeFrame = (): Buffer | undefined => {
    if (!postponed || framed) return undefined;
    framed = true;
    return postponed;
  };

  const writeHead = res.writeHead;
  res.writeHead = function (this: http.ServerResponse, ...args: unknown[]) {
    if (postponed && !this.headersSent) {
      const headersAt = typeof args[1] === "string" ? 2 : 1;
      if (args.length > headersAt) args[headersAt] = dropContentLength(args[headersAt]);
    }
    stampHeaders();
    return (writeHead as (...a: unknown[]) => http.ServerResponse).apply(this, args);
  } as typeof res.writeHead;

  const write = res.write;
  res.write = function (this: http.ServerResponse, ...args: unknown[]) {
    stampHeaders();
    const frame = takeFrame();
    if (frame) (write as (...a: unknown[]) => boolean).call(this, frame);
    return (write as (...a: unknown[]) => boolean).apply(this, args);
  } as typeof res.write;

  const end = res.end;
  res.end = function (this: http.ServerResponse, ...args: unknown[]) {
    stampHeaders();
    const frame = takeFrame();
    if (frame) (write as (...a: unknown[]) => boolean).call(this, frame);
    return (end as (...a: unknown[]) => http.ServerResponse).apply(this, args);
  } as typeof res.end;

  return {
    minimalMode: true,
    async onCacheEntry(entry) {
      const state = entry.value?.postponed;
      if (typeof state === "string") postponed = Buffer.from(state, "utf8");
      return false;
    },
  };
}

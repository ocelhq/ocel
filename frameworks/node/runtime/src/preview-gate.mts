import { readFile } from "node:fs/promises";
import http from "node:http";
import type { Duplex } from "node:stream";
import {
  checkPreview,
  decodeMountedPreviewGate,
  PREVIEW_BYPASS_HEADER,
  PREVIEW_GATE_CACHE_CONTROL,
  PREVIEW_ROBOTS_TAG,
  PREVIEW_SET_BYPASS_COOKIE_HEADER,
  type PreviewGate,
  type PreviewResponse,
} from "@platform/edge-contract/preview-gate";

export const PREVIEW_GATE_CACHE_MS = 5000;

type Reading = { kind: "gated"; gate: PreviewGate } | { kind: "open" } | { kind: "unreadable" };

export interface PreviewGateSource {
  current(): Promise<Reading>;
  nowSeconds(): number;
}

export const PREVIEW_GATE_READ_TIMEOUT_MS = 2000;

export function newPreviewGateSource(
  read: () => Promise<string>,
  nowMs: () => number,
  readTimeoutMs = PREVIEW_GATE_READ_TIMEOUT_MS,
): PreviewGateSource {
  let cached: { reading: Reading; at: number } | undefined;
  let loading: Promise<Reading> | undefined;
  const load = async (): Promise<Reading> => {
    try {
      const gate = decodeMountedPreviewGate(await read());
      return gate ? { kind: "gated", gate } : { kind: "open" };
    } catch {
      return { kind: "unreadable" };
    }
  };
  const refresh = (started: number): Promise<Reading> =>
    load()
      .then((reading) => {
        cached = { reading, at: started };
        return reading;
      })
      .finally(() => {
        loading = undefined;
      });
  return {
    nowSeconds: () => Math.floor(nowMs() / 1000),
    async current() {
      const now = nowMs();
      if (cached && now - cached.at < PREVIEW_GATE_CACHE_MS) return cached.reading;
      loading ??= refresh(now);
      let timer: NodeJS.Timeout | undefined;
      const timedOut = new Promise<Reading>((resolve) => {
        timer = setTimeout(() => resolve({ kind: "unreadable" }), readTimeoutMs);
      });
      try {
        return await Promise.race([loading, timedOut]);
      } finally {
        clearTimeout(timer);
      }
    },
  };
}

export interface MountedGate {
  file?: string;
  inline?: string;
}

export function openPreviewGate(mounted: MountedGate): PreviewGateSource {
  const { file, inline } = mounted;
  if (file !== undefined) return newPreviewGateSource(() => readFile(file, "utf8"), Date.now);
  return newPreviewGateSource(async () => inline ?? "", Date.now);
}

function newUnavailableResponse(): PreviewResponse {
  return {
    status: 503,
    headers: new Headers({
      "content-type": "text/plain; charset=utf-8",
      "cache-control": PREVIEW_GATE_CACHE_CONTROL,
      "x-robots-tag": PREVIEW_ROBOTS_TAG,
    }),
    body: "Service temporarily unavailable.",
  };
}

async function checkGate(
  source: PreviewGateSource,
  req: http.IncomingMessage,
  reachedHost: string,
): Promise<PreviewResponse | undefined | "open"> {
  const reading = await source.current();
  if (reading.kind === "unreadable") return newUnavailableResponse();
  if (reading.kind === "open") return "open";
  const verdict = await checkPreview(
    reading.gate,
    {
      method: req.method ?? "GET",
      host: reachedHost,
      target: req.url ?? "/",
      headers: newFetchHeaders(req),
    },
    source.nowSeconds(),
  );
  if (verdict.kind === "respond") return verdict.response;
  replaceCredentialHeaders(req, verdict.headers);
  return undefined;
}

export async function admitPreview(
  source: PreviewGateSource,
  req: http.IncomingMessage,
  res: http.ServerResponse,
  reachedHost = req.headers.host ?? "",
): Promise<boolean> {
  const answer = await checkGate(source, req, reachedHost);
  if (answer === "open") return true;
  if (answer) {
    const { status, headers, body } = answer;
    res.writeHead(status, Object.fromEntries(headers));
    res.end(body);
    return false;
  }
  markResponseNoindex(res);
  return true;
}

export async function admitPreviewUpgrade(
  source: PreviewGateSource,
  req: http.IncomingMessage,
  socket: Duplex,
  reachedHost = req.headers.host ?? "",
): Promise<boolean> {
  const answer = await checkGate(source, req, reachedHost);
  if (answer === "open" || answer === undefined) return true;
  writeUpgradeAnswer(socket, answer);
  return false;
}

function writeUpgradeAnswer(socket: Duplex, answer: PreviewResponse): void {
  const { status, headers, body } = answer;
  headers.set("connection", "close");
  headers.set("content-length", String(Buffer.byteLength(body)));
  let head = `HTTP/1.1 ${status} ${http.STATUS_CODES[status] ?? ""}\r\n`;
  for (const [name, value] of headers) head += `${name}: ${value}\r\n`;
  socket.end(`${head}\r\n${body}`);
}

function markResponseNoindex(res: http.ServerResponse): void {
  const writeHead = res.writeHead;
  res.writeHead = function (this: http.ServerResponse, ...args: any[]) {
    if (!this.headersSent) {
      const last = args.at(-1);
      if (typeof last === "object" && last !== null) {
        setHeaders(this, last);
        args.pop();
      }
      this.setHeader("x-robots-tag", PREVIEW_ROBOTS_TAG);
    }
    return (writeHead as any).apply(this, args);
  } as typeof res.writeHead;
}

function setHeaders(res: http.ServerResponse, headers: object): void {
  if (!Array.isArray(headers)) {
    for (const [name, value] of Object.entries(headers)) res.setHeader(name, value);
    return;
  }
  if (Array.isArray(headers[0])) {
    for (const [name, value] of headers as [string, string][]) res.appendHeader(name, value);
    return;
  }
  for (let i = 0; i + 1 < headers.length; i += 2) res.removeHeader(headers[i]);
  for (let i = 0; i + 1 < headers.length; i += 2) res.appendHeader(headers[i], headers[i + 1]);
}

function newFetchHeaders(req: http.IncomingMessage): Headers {
  const headers = new Headers();
  for (let i = 0; i + 1 < req.rawHeaders.length; i += 2) {
    headers.append(req.rawHeaders[i]!, req.rawHeaders[i + 1]!);
  }
  return headers;
}

const credentialHeaders = new Set([
  "authorization",
  "cookie",
  PREVIEW_BYPASS_HEADER,
  PREVIEW_SET_BYPASS_COOKIE_HEADER,
]);

function replaceCredentialHeaders(req: http.IncomingMessage, admitted: Headers): void {
  const raw: string[] = [];
  for (let i = 0; i + 1 < req.rawHeaders.length; i += 2) {
    if (!credentialHeaders.has(req.rawHeaders[i]!.toLowerCase())) {
      raw.push(req.rawHeaders[i]!, req.rawHeaders[i + 1]!);
    }
  }
  for (const name of credentialHeaders) {
    delete req.headers[name];
    const value = admitted.get(name);
    if (value === null) continue;
    req.headers[name] = value;
    raw.push(name, value);
  }
  req.rawHeaders = raw;
}

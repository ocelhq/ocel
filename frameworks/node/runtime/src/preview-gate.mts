import http from "node:http";
import type { Duplex } from "node:stream";
import {
  checkPreview,
  PREVIEW_GATE_CACHE_CONTROL,
  PREVIEW_LOGIN_BODY_MAX_BYTES,
  PREVIEW_LOGIN_PATH,
  PREVIEW_ROBOTS_TAG,
  type PreviewGate,
  type PreviewResponse,
  parsePreviewGate,
} from "@platform/edge-contract/preview-gate";

const oversizedLoginBody = new Uint8Array(PREVIEW_LOGIN_BODY_MAX_BYTES + 1);

type OriginGate = { kind: "gated"; gate: PreviewGate } | { kind: "unreadable" };

const readGates = new Map<string, OriginGate>();

function readOriginGate(encoded: string): OriginGate {
  let read = readGates.get(encoded);
  if (read === undefined) {
    const gate = parsePreviewGate(parseJson(encoded));
    read = gate ? { kind: "gated", gate } : { kind: "unreadable" };
    readGates.set(encoded, read);
  }
  return read;
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

async function checkOriginGate(
  encoded: string,
  req: http.IncomingMessage,
  reachedHost: string,
): Promise<PreviewResponse | undefined> {
  const origin = readOriginGate(encoded);
  if (origin.kind === "unreadable") return newUnavailableResponse();
  const target = req.url ?? "/";
  const body =
    req.method === "POST" && target.split("?", 1)[0] === PREVIEW_LOGIN_PATH
      ? ((await readCapped(req, PREVIEW_LOGIN_BODY_MAX_BYTES)) ?? oversizedLoginBody)
      : undefined;
  const verdict = await checkPreview(
    origin.gate,
    {
      method: req.method ?? "GET",
      host: reachedHost,
      target,
      headers: newFetchHeaders(req.headers),
      body,
    },
    Math.floor(Date.now() / 1000),
  );
  if (verdict.kind === "respond") return verdict.response;
  replaceHeaders(req, verdict.headers);
  return undefined;
}

export async function admitPreview(
  encoded: string,
  req: http.IncomingMessage,
  reachedHost: string,
  res: http.ServerResponse,
): Promise<boolean> {
  const answer = await checkOriginGate(encoded, req, reachedHost);
  if (answer) {
    const { status, headers, body } = answer;
    if (status === 413) headers.set("connection", "close");
    res.writeHead(status, Object.fromEntries(headers));
    res.end(body);
    return false;
  }
  markResponseNoindex(res);
  return true;
}

export async function admitPreviewUpgrade(
  encoded: string,
  req: http.IncomingMessage,
  reachedHost: string,
  socket: Duplex,
): Promise<boolean> {
  const answer = await checkOriginGate(encoded, req, reachedHost);
  if (answer) writeUpgradeAnswer(socket, answer);
  return answer === undefined;
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
    for (const [name, value] of headers as [string, string][]) res.setHeader(name, value);
    return;
  }
  for (let i = 0; i + 1 < headers.length; i += 2) res.setHeader(headers[i], headers[i + 1]);
}

function newFetchHeaders(incoming: http.IncomingHttpHeaders): Headers {
  const headers = new Headers();
  for (const [name, value] of Object.entries(incoming)) {
    if (value === undefined) continue;
    for (const each of Array.isArray(value) ? value : [value]) headers.append(name, each);
  }
  return headers;
}

function replaceHeaders(req: http.IncomingMessage, admitted: Headers): void {
  for (const name of Object.keys(req.headers)) delete req.headers[name];
  const raw: string[] = [];
  for (const [name, value] of admitted) {
    req.headers[name] = value;
    raw.push(name, value);
  }
  req.rawHeaders = raw;
}

function readCapped(req: http.IncomingMessage, max: number): Promise<Uint8Array | undefined> {
  const contentLength = Number(req.headers["content-length"]);
  if (contentLength > max) return Promise.resolve(undefined);
  return new Promise((resolve) => {
    const chunks: Buffer[] = [];
    let total = 0;
    const onData = (chunk: Buffer): void => {
      total += chunk.length;
      if (total > max) {
        req.off("data", onData);
        resolve(undefined);
        return;
      }
      chunks.push(chunk);
    };
    req.on("data", onData);
    req.once("end", () => resolve(Buffer.concat(chunks)));
    req.once("error", () => resolve(undefined));
  });
}

function parseJson(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

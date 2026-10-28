import type { ChildProcess } from "node:child_process";
import { subscribe } from "node:diagnostics_channel";
import { type ClientRequest, type IncomingMessage, STATUS_CODES } from "node:http";
import { basename } from "node:path";
import {
  type Attributes,
  type Context,
  type Span,
  SpanKind,
  SpanStatusCode,
  type Tracer,
} from "@opentelemetry/api";

export interface Recording {
  tracer: Tracer;
  currentContext(): Context;
  isExporting(): boolean;
  recordsSuccessfulBodies: boolean;
}

interface SpawnOptions {
  file: string;
  args: string[];
  cwd?: string;
  envPairs?: string[];
}

function explicitEnv(envPairs: readonly string[] | undefined): Attributes {
  const attrs: Attributes = {};
  for (const pair of envPairs ?? []) {
    const at = pair.indexOf("=");
    if (at <= 0) continue;
    const name = pair.slice(0, at);
    const value = pair.slice(at + 1);
    if (process.env[name] === value) continue;
    attrs[`process.env.${name}`] = value;
  }
  return attrs;
}

export function recordChildProcesses({ tracer, currentContext }: Recording): void {
  subscribe("tracing:child_process.spawn:start", (message) => {
    const { process: child, options } = message as { process: ChildProcess; options: SpawnOptions };
    const span = tracer.startSpan(
      basename(options.file),
      {
        attributes: {
          "process.executable.path": options.file,
          "process.command_args": options.args,
          "process.working_directory": options.cwd || process.cwd(),
          ...explicitEnv(options.envPairs),
        },
      },
      currentContext(),
    );
    child.once("spawn", () => {
      if (child.pid !== undefined) span.setAttribute("process.pid", child.pid);
    });
    child.once("exit", (code, signal) => {
      if (code !== null) span.setAttribute("process.exit.code", code);
      if (signal !== null) span.setAttribute("process.exit.signal", signal);
      if (code !== 0) {
        span.setStatus({
          code: SpanStatusCode.ERROR,
          message: signal ? `killed by ${signal}` : `exited with code ${code}`,
        });
      }
      span.end();
    });
    const failed = (err: Error) => {
      failSpan(span, err);
      span.end();
      if (child.listenerCount("error") === 0) throw err;
    };
    child.once("error", failed);
  });
}

function headerAttributes(prefix: string, headers: Iterable<[string, unknown]>): Attributes {
  const attrs: Attributes = {};
  for (const [name, value] of headers) {
    if (value === undefined) continue;
    attrs[`${prefix}${name.toLowerCase()}`] = (Array.isArray(value) ? value : [value]).map(String);
  }
  return attrs;
}

export function failSpan(span: Span, err: unknown): void {
  const error = err instanceof Error ? err : new Error(String(err));
  span.recordException(error);
  span.setStatus({ code: SpanStatusCode.ERROR, message: error.message });
}

const recordedBodyBytes = 8 << 10;

const grantType = /grant_?type/i;

const textualTypes = [
  "json",
  "xml",
  "text/",
  "x-www-form-urlencoded",
  "yaml",
  "graphql",
  "javascript",
];

function isTextual(contentType: unknown): boolean {
  const lower = String(contentType ?? "").toLowerCase();
  return textualTypes.some((textual) => lower.includes(textual));
}

function bytesOf(chunk: unknown, encoding: unknown): Buffer | undefined {
  if (typeof chunk === "string") {
    return Buffer.from(
      chunk,
      typeof encoding === "string" && Buffer.isEncoding(encoding) ? encoding : "utf8",
    );
  }
  if (chunk instanceof Uint8Array)
    return Buffer.from(chunk.buffer, chunk.byteOffset, chunk.byteLength);
  return undefined;
}

class Body {
  size = 0;
  private prefix: Buffer[] = [];
  private prefixBytes = 0;
  private truncated = false;

  record(chunk: unknown, encoding?: unknown): void {
    const bytes = bytesOf(chunk, encoding);
    if (!bytes) return;
    this.size += bytes.byteLength;
    const room = recordedBodyBytes - this.prefixBytes;
    if (bytes.byteLength > room) this.truncated = true;
    if (room <= 0) return;
    const kept = Buffer.from(bytes.subarray(0, room));
    this.prefix.push(kept);
    this.prefixBytes += kept.byteLength;
  }

  text(): string {
    return Buffer.concat(this.prefix).toString("utf8");
  }

  attributes(key: string): Attributes {
    return { [key]: this.text(), [`${key}.truncated`]: this.truncated };
  }
}

class Exchange {
  sent: Body | undefined;
  sentType: unknown;
  received: Body | undefined;
  receivedType: unknown;
  private failed = false;
  private error: unknown;

  constructor(
    readonly span: Span,
    private readonly recordsSuccessfulBodies: boolean,
  ) {}

  respond(status: number, reason: string | undefined): void {
    this.span.setAttribute("http.response.status_code", status);
    if (status < 400) return;
    this.failed = true;
    this.span.setStatus({
      code: SpanStatusCode.ERROR,
      message: `${status} ${reason || STATUS_CODES[status] || ""}`.trim(),
    });
  }

  fail(error: unknown): void {
    this.failed = true;
    this.error = error;
  }

  end(): void {
    const { span, sent, received } = this;
    if (received) span.setAttribute("http.response.body.size", received.size);
    if ((this.failed || this.recordsSuccessfulBodies) && !grantType.test(sent?.text() ?? "")) {
      if (sent && isTextual(this.sentType))
        span.setAttributes(sent.attributes("http.request.body"));
      if (received && isTextual(this.receivedType)) {
        span.setAttributes(received.attributes("http.response.body"));
      }
    }
    if (this.error !== undefined) failSpan(span, this.error);
    span.end();
  }
}

function recordWrites(request: ClientRequest): Body {
  const body = new Body();
  for (const method of ["write", "end"] as const) {
    const original = request[method] as (...args: unknown[]) => unknown;
    request[method] = ((...args: unknown[]) => {
      body.record(args[0], args[1]);
      return original.apply(request, args);
    }) as never;
  }
  return body;
}

function recordPushes(response: IncomingMessage): Body {
  const body = new Body();
  const push = response.push;
  response.push = (chunk: unknown, encoding?: BufferEncoding) => {
    body.record(chunk, encoding);
    return push.call(response, chunk, encoding);
  };
  return body;
}

export function recordHTTPRequests({
  tracer,
  currentContext,
  isExporting,
  recordsSuccessfulBodies,
}: Recording): void {
  const sentBodies = new WeakMap<ClientRequest, Body>();
  const exchanges = new WeakMap<ClientRequest, Exchange>();
  subscribe("http.client.request.created", (message) => {
    if (isExporting()) return;
    const { request } = message as { request: ClientRequest };
    sentBodies.set(request, recordWrites(request));
  });
  subscribe("http.client.request.start", (message) => {
    if (isExporting()) return;
    const { request } = message as { request: ClientRequest };
    const host = String(request.getHeader("host") ?? request.host);
    const exchange = new Exchange(
      tracer.startSpan(
        `${request.method} ${host}`,
        {
          kind: SpanKind.CLIENT,
          attributes: {
            "http.request.method": request.method,
            "url.full": `${request.protocol}//${host}${request.path}`,
            "server.address": request.host,
            ...headerAttributes("http.request.header.", Object.entries(request.getHeaders())),
          },
        },
        currentContext(),
      ),
      recordsSuccessfulBodies,
    );
    exchange.sent = sentBodies.get(request);
    exchange.sentType = request.getHeader("content-type");
    exchanges.set(request, exchange);
    request.once("close", () => exchange.end());
  });
  subscribe("http.client.response.finish", (message) => {
    const { request, response } = message as { request: ClientRequest; response: IncomingMessage };
    const exchange = exchanges.get(request);
    if (!exchange) return;
    exchange.span.setAttributes(
      headerAttributes("http.response.header.", Object.entries(response.headers)),
    );
    exchange.respond(response.statusCode ?? 0, response.statusMessage);
    exchange.received = recordPushes(response);
    exchange.receivedType = response.headers["content-type"];
  });
  subscribe("http.client.request.error", (message) => {
    const { request, error } = message as { request: ClientRequest; error: unknown };
    exchanges.get(request)?.fail(error);
  });
}

interface FetchRequest {
  origin: string;
  method: string;
  path: string;
  headers: string | string[] | Buffer[];
}

function fetchHeaders(headers: FetchRequest["headers"] | undefined): [string, string][] {
  if (typeof headers === "string") {
    return headers
      .split("\r\n")
      .filter(Boolean)
      .map((line) => {
        const at = line.indexOf(":");
        return [line.slice(0, at).trim(), line.slice(at + 1).trim()];
      });
  }
  const pairs: [string, string][] = [];
  for (let i = 0; i + 1 < (headers?.length ?? 0); i += 2) {
    pairs.push([String(headers?.[i]), String(headers?.[i + 1])]);
  }
  return pairs;
}

function contentTypeOf(headers: [string, string][]): string | undefined {
  return headers.find(([name]) => name.toLowerCase() === "content-type")?.[1];
}

function publishesBodyChunks(undici = ""): boolean {
  const [major = 0, minor = 0] = undici.split(".").map(Number);
  return major > 7 || (major === 7 && minor >= 11);
}

export function recordFetches({
  tracer,
  currentContext,
  recordsSuccessfulBodies,
}: Recording): void {
  const bodyChunks = publishesBodyChunks(process.versions.undici);
  const exchanges = new WeakMap<FetchRequest, Exchange>();
  subscribe("undici:request:create", (message) => {
    const { request } = message as { request: FetchRequest };
    const url = new URL(request.path, request.origin);
    const headers = fetchHeaders(request.headers);
    const exchange = new Exchange(
      tracer.startSpan(
        `${request.method} ${url.host}`,
        {
          kind: SpanKind.CLIENT,
          attributes: {
            "http.request.method": request.method,
            "url.full": url.href,
            "server.address": url.hostname,
            ...headerAttributes("http.request.header.", headers),
          },
        },
        currentContext(),
      ),
      recordsSuccessfulBodies,
    );
    if (bodyChunks) {
      exchange.sent = new Body();
      exchange.sentType = contentTypeOf(headers);
    }
    exchanges.set(request, exchange);
  });
  subscribe("undici:request:bodyChunkSent", (message) => {
    const { request, chunk } = message as { request: FetchRequest; chunk: unknown };
    exchanges.get(request)?.sent?.record(chunk);
  });
  subscribe("undici:request:headers", (message) => {
    const { request, response } = message as {
      request: FetchRequest;
      response: { statusCode: number; statusText?: string; headers: FetchRequest["headers"] };
    };
    const exchange = exchanges.get(request);
    if (!exchange) return;
    const headers = fetchHeaders(response.headers);
    exchange.span.setAttributes(headerAttributes("http.response.header.", headers));
    exchange.respond(response.statusCode, response.statusText);
    if (bodyChunks) {
      exchange.received = new Body();
      exchange.receivedType = contentTypeOf(headers);
    }
  });
  subscribe("undici:request:bodyChunkReceived", (message) => {
    const { request, chunk } = message as { request: FetchRequest; chunk: unknown };
    exchanges.get(request)?.received?.record(chunk);
  });
  subscribe("undici:request:trailers", (message) => {
    const { request } = message as { request: FetchRequest };
    exchanges.get(request)?.end();
  });
  subscribe("undici:request:error", (message) => {
    const { request, error } = message as { request: FetchRequest; error: unknown };
    const exchange = exchanges.get(request);
    if (!exchange) return;
    exchange.fail(error);
    exchange.end();
  });
}

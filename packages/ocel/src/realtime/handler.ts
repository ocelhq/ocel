import {
  type DeclaredChannel,
  encodeEvent,
  isParams,
  type Realtime,
  type RealtimeRuntime,
  readRuntime,
} from "./realtime.js";
import { mintToken } from "./token.js";
import { resolveTransport, type Transport } from "./transport.js";
import { encodeWireChannel, type WireRefusal } from "./wire.js";

/** How a realtime handler serves browsers. */
export interface RealtimeHandlerOptions {
  /**
   * Origins besides the handler's own that may call it, such as `"https://app.example"`.
   * Each is answered with CORS headers; without any, only the handler's own origin is
   * served and no CORS header is ever sent.
   */
  allowedOrigins?: string[];
}

/** The Web `Request` to `Response` handlers serving a realtime resource, one per method. */
export interface RealtimeHandler {
  /** Serves a batch of `connect`, `subscribe` and `publish` requests. */
  POST(request: Request): Promise<Response>;
  /** Refuses with 405: the handler takes POST alone. */
  GET(request: Request): Promise<Response>;
  /** Answers the CORS preflight of an origin in `allowedOrigins`, and refuses any other. */
  OPTIONS(request: Request): Promise<Response>;
}

/** The most bytes a realtime handler reads of a request body before refusing it with 413. */
export const maxRequestBytes = 1024 * 1024;
const maxOps = 50;
const opKeys = new Set(["op", "pattern", "params", "body"]);

type OpName = "subscribe" | "publish";

type RealtimeDenial =
  | "invalid-op"
  | "unknown-op"
  | "unknown-pattern"
  | "no-publish-rule"
  | "invalid-params"
  | WireRefusal
  | "invalid-body"
  | "body-too-large"
  | "unauthenticated"
  | "forbidden"
  | "rule-error"
  | "publish-failed";

type Grant = { i: number; wire: string; token?: string };
type Denial = { i: number; code: RealtimeDenial };
type Outcome = { grant: Grant } | { denied: Denial };

interface BatchRequest {
  runtime: RealtimeRuntime;
  text: string;
  request: Request;
  auth: unknown;
  subject: string;
  properties: ReturnType<RealtimeRuntime["properties"]>;
  transport: Transport;
}

function respond(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: {
      "cache-control": "no-store",
      ...(body === undefined ? {} : { "content-type": "application/json" }),
      ...headers,
    },
  });
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function readFirstHeaderValue(request: Request, name: string): string | undefined {
  return request.headers.get(name)?.split(",")[0]?.trim() || undefined;
}

function readOwnOrigin(request: Request): { scheme: string; host: string } {
  const url = new URL(request.url);
  return {
    scheme:
      readFirstHeaderValue(request, "x-forwarded-proto")?.toLowerCase() ??
      url.protocol.slice(0, -1),
    host:
      readFirstHeaderValue(request, "x-forwarded-host") ??
      (request.headers.get("host") || url.host),
  };
}

type OriginVerdict = { allowed: false } | { allowed: true; cors: Record<string, string> };

function judgeOrigin(request: Request, allowedOrigins: readonly string[]): OriginVerdict {
  const origin = request.headers.get("origin");
  if (origin === null) return { allowed: true, cors: {} };
  if (allowedOrigins.includes(origin)) {
    return {
      allowed: true,
      cors: { "access-control-allow-origin": origin, vary: "Origin" },
    };
  }
  let url: URL;
  try {
    url = new URL(origin);
  } catch {
    return { allowed: false };
  }
  const own = readOwnOrigin(request);
  return url.host !== "" && url.protocol === `${own.scheme}:` && url.host === own.host
    ? { allowed: true, cors: {} }
    : { allowed: false };
}

function readSubject(auth: unknown): string {
  const id = (auth as { id?: unknown } | null | undefined)?.id;
  return typeof id === "string" || typeof id === "number" ? String(id) : "anonymous";
}

async function readBoundedText(request: Request): Promise<string | undefined> {
  const reader = request.body?.getReader();
  if (!reader) return "";
  const chunks: Uint8Array[] = [];
  let bytes = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    bytes += value.byteLength;
    if (bytes > maxRequestBytes) {
      await reader.cancel();
      return undefined;
    }
    chunks.push(value);
  }
  const joined = new Uint8Array(bytes);
  let offset = 0;
  for (const chunk of chunks) {
    joined.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder().decode(joined);
}

function copyRequest(request: Request, text: string): Request {
  return new Request(request, { body: text });
}

function parseBatch(text: string): { connect: boolean; ops: unknown[] } | undefined {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    return undefined;
  }
  if (!isPlainObject(value)) return undefined;
  if (Object.keys(value).some((key) => key !== "connect" && key !== "ops")) return undefined;
  const { connect, ops } = value;
  if (!Array.isArray(ops) || (connect !== undefined && typeof connect !== "boolean")) {
    return undefined;
  }
  return { connect: connect ?? false, ops };
}

async function runRule(
  rule: () => boolean | Promise<boolean>,
): Promise<"allowed" | "forbidden" | "rule-error"> {
  try {
    return (await rule()) ? "allowed" : "forbidden";
  } catch {
    return "rule-error";
  }
}

async function evaluateOp(batch: BatchRequest, value: unknown, i: number): Promise<Outcome> {
  const deny = (code: RealtimeDenial): Outcome => ({ denied: { i, code } });
  if (
    !isPlainObject(value) ||
    Object.keys(value).some((key) => !opKeys.has(key)) ||
    (value.op === "subscribe" && Object.hasOwn(value, "body"))
  ) {
    return deny("invalid-op");
  }
  const op = value.op;
  if (op !== "subscribe" && op !== "publish") return deny("unknown-op");
  const channel =
    typeof value.pattern === "string" ? batch.runtime.channels.get(value.pattern) : undefined;
  if (!channel) return deny("unknown-pattern");
  if (op === "publish" && !channel.publish) return deny("no-publish-rule");
  const params = value.params ?? {};
  if (!isParams(params)) return deny("invalid-params");
  const wire = encodeWireChannel(
    batch.runtime.name,
    channel.pattern,
    params,
    op === "subscribe" && channel.wildcard,
  );
  if ("refused" in wire) return deny(wire.refused);
  return op === "subscribe"
    ? evaluateSubscribe(batch, channel, params, wire.channel, i)
    : evaluatePublish(batch, channel, params, wire.channel, value, i);
}

async function evaluateSubscribe(
  batch: BatchRequest,
  channel: DeclaredChannel,
  params: Record<string, string>,
  wire: string,
  i: number,
): Promise<Outcome> {
  const rule = channel.subscribe;
  if (rule !== "public") {
    if (batch.auth === null) return { denied: { i, code: "unauthenticated" } };
    const request = copyRequest(batch.request, batch.text);
    const verdict = await runRule(() => rule({ auth: batch.auth, params, request }));
    if (verdict !== "allowed") return { denied: { i, code: verdict } };
  }
  const { token } = mintToken(batch.properties, batch.runtime, batch.subject, "subscribe", wire);
  return { grant: { i, wire, token } };
}

async function evaluatePublish(
  batch: BatchRequest,
  channel: DeclaredChannel,
  params: Record<string, string>,
  wire: string,
  op: Record<string, unknown>,
  i: number,
): Promise<Outcome> {
  const rule = channel.publish;
  if (!rule) return { denied: { i, code: "no-publish-rule" } };
  if (!Object.hasOwn(op, "body")) return { denied: { i, code: "invalid-body" } };
  const event = await encodeEvent(channel, wire, op.body);
  if ("refused" in event) return { denied: { i, code: event.refused } };
  if (batch.auth === null) return { denied: { i, code: "unauthenticated" } };
  const request = copyRequest(batch.request, batch.text);
  const verdict = await runRule(() =>
    rule({ auth: batch.auth, params, body: event.body, request }),
  );
  if (verdict !== "allowed") return { denied: { i, code: verdict } };
  const send = batch.transport.signPublish(event.channel);
  try {
    await send(event.envelope);
  } catch {
    return { denied: { i, code: "publish-failed" } };
  }
  return { grant: { i, wire } };
}

function isOpNamed(value: unknown, name: OpName): boolean {
  return isPlainObject(value) && value.op === name;
}

function evaluateOps(batch: BatchRequest, ops: unknown[]): Promise<Outcome[]> {
  let publishing: Promise<unknown> = Promise.resolve();
  return Promise.all(
    ops.map((value, i) => {
      if (!isOpNamed(value, "publish")) return evaluateOp(batch, value, i);
      const outcome = publishing.then(() => evaluateOp(batch, value, i));
      publishing = outcome.catch(() => undefined);
      return outcome;
    }),
  );
}

async function serveBatch(
  runtime: RealtimeRuntime,
  request: Request,
  cors: Record<string, string>,
): Promise<Response> {
  const mediaType = request.headers.get("content-type")?.split(";")[0]?.trim().toLowerCase();
  if (mediaType !== "application/json") {
    return respond(415, { error: "the realtime handler takes application/json" }, cors);
  }
  const tooLarge = () =>
    respond(413, { error: `a request is at most ${maxRequestBytes} bytes` }, cors);
  if (Number(request.headers.get("content-length") ?? 0) > maxRequestBytes) return tooLarge();
  const text = await readBoundedText(request);
  if (text === undefined) return tooLarge();
  const parsed = parseBatch(text);
  if (!parsed) {
    return respond(
      400,
      { error: "the body is { connect?: boolean, ops: [{ op, pattern, params?, body? }] }" },
      cors,
    );
  }
  if (parsed.ops.length > maxOps) {
    return respond(400, { error: `a request holds at most ${maxOps} ops` }, cors);
  }

  const properties = runtime.properties("handler");
  const transport = resolveTransport(properties, runtime);
  let auth: unknown = null;
  if (runtime.authorize) {
    try {
      auth = (await runtime.authorize(copyRequest(request, text))) ?? null;
    } catch {
      return respond(500, { error: "authorize failed" }, cors);
    }
  }
  const batch: BatchRequest = {
    runtime,
    text,
    request,
    auth,
    subject: readSubject(auth),
    properties,
    transport,
  };

  const connect = parsed.connect
    ? mintToken(properties, runtime, batch.subject, "connect", `/${runtime.name}`)
    : undefined;
  const outcomes = await evaluateOps(batch, parsed.ops);
  return respond(
    200,
    {
      transport: transport.name,
      url: properties.url,
      ...(transport.host === undefined ? {} : { host: transport.host }),
      ...(connect === undefined ? {} : { connect }),
      grants: outcomes.flatMap((o) => ("grant" in o ? [o.grant] : [])),
      denied: outcomes.flatMap((o) => ("denied" in o ? [o.denied] : [])),
    },
    cors,
  );
}

/**
 * Serves `rt` to browsers as Web `Request` to `Response` handlers, for a framework that
 * speaks the Fetch API. It takes `POST` with `application/json` alone, serves its own
 * origin and those in `allowedOrigins`, answers `Cache-Control: no-store`, and never sets
 * a cookie.
 */
export function createWebRealtimeHandler(
  rt: Realtime,
  options: RealtimeHandlerOptions = {},
): RealtimeHandler {
  const runtime = readRuntime(rt);
  const allowedOrigins = options.allowedOrigins ?? [];
  const respondMethodNotAllowed = () =>
    respond(405, { error: "the realtime handler takes POST" }, { allow: "POST" });

  return {
    async POST(request) {
      let cors: Record<string, string> = {};
      try {
        const verdict = judgeOrigin(request, allowedOrigins);
        if (!verdict.allowed) {
          return respond(403, { error: "this origin may not call the realtime handler" });
        }
        cors = verdict.cors;
        return await serveBatch(runtime, request, cors);
      } catch {
        return respond(500, { error: "the realtime handler failed" }, cors);
      }
    },
    async GET() {
      return respondMethodNotAllowed();
    },
    async OPTIONS(request) {
      const origin = request.headers.get("origin");
      if (origin === null || !allowedOrigins.includes(origin)) return respondMethodNotAllowed();
      return respond(204, undefined, {
        "access-control-allow-origin": origin,
        "access-control-allow-methods": "POST",
        "access-control-allow-headers": "authorization, content-type",
        "access-control-max-age": "600",
        vary: "Origin",
      });
    },
  };
}

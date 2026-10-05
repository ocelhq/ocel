import { createHash, timingSafeEqual } from "node:crypto";
import http from "node:http";
import Module from "node:module";
import net from "node:net";

let controlSocket: net.Socket | null = null;
const controlHandlers = new Set<(message: unknown) => void>();

export function hasControl(): boolean {
  return Boolean(process.env.OCEL_CONTROL_SOCKET);
}

function control(): net.Socket | null {
  if (!hasControl()) return null;
  if (!controlSocket) {
    controlSocket = net.createConnection(process.env.OCEL_CONTROL_SOCKET!);
    receive(controlSocket);
  }
  return controlSocket;
}

export function sendControl(type: string, payload: unknown): void {
  control()?.write(`${JSON.stringify({ type, payload })}\n`);
}

export function onControlMessage(handler: (message: unknown) => void): void {
  controlHandlers.add(handler);
  control();
}

function receive(socket: net.Socket): void {
  let buffer = "";
  socket.on("data", (chunk) => {
    buffer += chunk.toString();
    for (;;) {
      const end = buffer.indexOf("\n");
      if (end < 0) break;
      const line = buffer.slice(0, end);
      buffer = buffer.slice(end + 1);
      if (!line.trim()) continue;
      let message: unknown;
      try {
        message = JSON.parse(line);
      } catch {
        continue;
      }
      for (const handler of controlHandlers) handler(message);
    }
  });
}

export function reportFatalBoot(err: unknown): void {
  const detail = err instanceof Error ? (err.stack ?? err.message) : String(err);
  console.error(`ocel: fatal boot error: ${detail}`);
}

function flushCompileCacheNow(): { dir: string | null; ok: boolean } {
  let dir: string | null = null;
  try {
    const { getCompileCacheDir, flushCompileCache } = Module;
    dir = typeof getCompileCacheDir === "function" ? (getCompileCacheDir() ?? null) : null;
    if (typeof flushCompileCache !== "function") return { dir, ok: false };
    flushCompileCache();
    return { dir, ok: typeof dir === "string" && dir.length > 0 };
  } catch {
    return { dir, ok: false };
  }
}

export function installCompileCacheFlush(): void {
  onControlMessage((message) => {
    if (!message || typeof message !== "object") return;
    if ((message as { type?: unknown }).type !== "flush-compile-cache") return;
    sendControl("compile-cache-flushed", flushCompileCacheNow());
  });
}

export interface WarmBounds {
  deadlineMs: number;
  ceilingBytes: number;
}

export interface WarmReport {
  ok: boolean;
  state: "warmed" | "unsupported";
  entries: number;
  loaded: number;
  failures: { entry: string; message: string }[];
  stoppedBy: "complete" | "deadline" | "ceiling" | "unmeasured";
  skipped: string[];
  skippedCount: number;
  bytes: number;
  dir: string | null;
}

export const UNSUPPORTED_WARM: WarmReport = {
  ok: false,
  state: "unsupported",
  entries: 0,
  loaded: 0,
  failures: [],
  stoppedBy: "complete",
  skipped: [],
  skippedCount: 0,
  bytes: 0,
  dir: null,
};

function warmNow(warm: unknown, payload: unknown): WarmReport {
  if (typeof warm !== "function") return UNSUPPORTED_WARM;
  const { deadlineMs, ceilingBytes } = (payload ?? {}) as Partial<WarmBounds>;
  try {
    const run = warm as (bounds: Partial<WarmBounds>) => WarmReport | undefined;
    return run({ deadlineMs, ceilingBytes }) ?? UNSUPPORTED_WARM;
  } catch (err) {
    sendControl("log", {
      level: "error",
      message: `compile cache warm failed: ${String(err)}`,
    });
    return UNSUPPORTED_WARM;
  }
}

export function installCompileCacheWarm(warm: unknown): void {
  onControlMessage((message) => {
    if (!message || typeof message !== "object") return;
    if ((message as { type?: unknown }).type !== "warm-compile-cache") return;
    sendControl("compile-cache-warmed", warmNow(warm, (message as { payload?: unknown }).payload));
  });
}

export interface OcelContext {
  waitUntil: (p: Promise<unknown>) => void;
  holdEnd: (p: Promise<unknown>) => void;
}

export type Invoke = (
  req: http.IncomingMessage,
  res: http.ServerResponse,
  ocel: OcelContext,
) => void | Promise<void>;

export async function drainWaitUntil(pending: Promise<unknown>[]): Promise<void> {
  while (pending.length > 0) {
    const batch = pending.splice(0, pending.length);
    const results = await Promise.allSettled(batch);
    for (const r of results) {
      if (r.status === "rejected") {
        sendControl("log", {
          level: "error",
          message: `waitUntil task failed: ${String(r.reason)}`,
        });
      }
    }
  }
}

const originDispatchVar = "OCEL_ORIGIN_DISPATCH";

export function dispatchesAtOrigin(env: NodeJS.ProcessEnv): boolean {
  return Boolean(env[originDispatchVar]);
}

const cacheTagPurgeVar = "OCEL_CACHE_TAG_PURGE";

const finishBeforeResponseVar = "OCEL_FINISH_BEFORE_RESPONSE_MS";

export function finishBeforeResponseMs(env: NodeJS.ProcessEnv): number {
  const capMs = Number(env[finishBeforeResponseVar]);
  return Number.isFinite(capMs) && capMs > 0 ? capMs : 0;
}

export function invalidatesByCacheTag(env: NodeJS.ProcessEnv): boolean {
  return Boolean(env[cacheTagPurgeVar]);
}

const originSecretVar = "OCEL_ORIGIN_SECRET";

const originSecretPreviousVar = "OCEL_ORIGIN_SECRET_PREVIOUS";

const originSignedVar = "OCEL_ORIGIN_SIGNED";

const originSecretHeader = "x-ocel-origin-secret";

type OriginGuard = (headers: http.IncomingHttpHeaders) => boolean;

function digest(value: string): Buffer {
  return createHash("sha256").update(value, "utf8").digest();
}

function presentedSecret(headers: http.IncomingHttpHeaders): string {
  const value = headers[originSecretHeader];
  if (Array.isArray(value)) return value[0] ?? "";
  return value ?? "";
}

function originGuard(env: NodeJS.ProcessEnv): OriginGuard | undefined {
  const secret = env[originSecretVar];
  const previous = env[originSecretPreviousVar];
  delete env[originSecretVar];
  delete env[originSecretPreviousVar];
  if (!dispatchesAtOrigin(env) || env[originSignedVar]) return undefined;
  if (!secret) return () => false;
  const expected = [secret, previous]
    .filter((value): value is string => Boolean(value))
    .map(digest);
  return (headers) => {
    const presented = digest(presentedSecret(headers));
    let matched = 0;
    for (const accepted of expected) matched |= timingSafeEqual(presented, accepted) ? 1 : 0;
    return matched === 1;
  };
}

interface Trust {
  entry?: boolean;
  forwarded?: boolean;
  guard?: OriginGuard;
}

function normalizeLoopbackHeaders(headers: http.IncomingHttpHeaders, trust: Trust): void {
  if (trust.forwarded) {
    const forwarded = String(headers["x-forwarded-host"] ?? "")
      .split(",")[0]
      ?.trim();
    if (forwarded) headers.host = forwarded;
  } else {
    delete headers["x-forwarded-host"];
    delete headers["x-forwarded-proto"];
  }
  if (!trust.entry && headers["x-ocel-request-id"] === undefined) {
    delete headers["x-ocel-entry"];
  }
  delete headers["x-ocel-request-id"];
  delete headers["x-ocel-trace-id"];
  delete headers[originSecretHeader];
}

async function settleWithin(held: Promise<unknown>[], capMs: number): Promise<void> {
  let timer: NodeJS.Timeout | undefined;
  const capped = new Promise<"capped">((resolve) => {
    timer = setTimeout(() => resolve("capped"), capMs);
  });
  const outcome = await Promise.race([drainWaitUntil(held), capped]);
  clearTimeout(timer);
  if (outcome === "capped") {
    sendControl("log", {
      level: "warn",
      message: `ending the response with background work still running after ${capMs}ms`,
    });
  }
}

function measureBytes(chunk: unknown, encoding: unknown): number {
  if (typeof chunk === "string") {
    return Buffer.byteLength(
      chunk,
      typeof encoding === "string" ? (encoding as BufferEncoding) : "utf8",
    );
  }
  return chunk instanceof Uint8Array ? chunk.byteLength : 0;
}

function parseContentLength(value: unknown): number | undefined {
  if (value === undefined || value === null || value === "") return undefined;
  const length = Number(Array.isArray(value) ? value[0] : value);
  return Number.isFinite(length) ? length : undefined;
}

function findContentLength(headers: unknown): number | undefined {
  if (Array.isArray(headers)) {
    for (let i = 0; i + 1 < headers.length; i += 2) {
      if (String(headers[i]).toLowerCase() === "content-length") {
        return parseContentLength(headers[i + 1]);
      }
    }
    return undefined;
  }
  if (!headers || typeof headers !== "object") return undefined;
  for (const [name, value] of Object.entries(headers)) {
    if (name.toLowerCase() === "content-length") return parseContentLength(value);
  }
  return undefined;
}

type WithheldCall = { method: (...args: any[]) => unknown; args: any[] };

interface ImplicitHead {
  _contentLength?: number | null;
  _implicitHeader?: () => void;
}

function writeImplicitHead(res: http.ServerResponse, endArgs: any[]): void {
  const internals = res as unknown as ImplicitHead;
  if (res.headersSent || typeof internals._implicitHeader !== "function") return;
  const chunk = typeof endArgs[0] === "function" ? undefined : endArgs[0];
  const encoding = typeof endArgs[1] === "string" ? endArgs[1] : undefined;
  internals._contentLength = chunk ? measureBytes(chunk, encoding) : 0;
  internals._implicitHeader();
}

function holdLastByte(res: http.ServerResponse, held: Promise<unknown>[], capMs: number): void {
  const end = res.end;
  const write = res.write;
  const writeHead = res.writeHead;
  let headDeclared: number | undefined;
  let written = 0;
  const withheld: WithheldCall[] = [];
  let ending = false;

  const replay = (): void => {
    for (const call of withheld.splice(0)) {
      try {
        call.method.apply(res, call.args);
      } catch (err) {
        sendControl("log", { level: "error", message: String((err as Error)?.stack || err) });
      }
    }
  };

  res.writeHead = function (this: http.ServerResponse, ...args: any[]) {
    headDeclared =
      findContentLength(typeof args[1] === "string" ? args[2] : args[1]) ?? headDeclared;
    return (writeHead as any).apply(this, args);
  } as typeof res.writeHead;

  res.write = function (this: http.ServerResponse, ...args: any[]) {
    if (ending) {
      withheld.push({ method: write, args });
      return false;
    }
    written += measureBytes(args[0], args[1]);
    const declared = parseContentLength(this.getHeader("content-length")) ?? headDeclared;
    if (withheld.length > 0 || (held.length > 0 && declared !== undefined && written >= declared)) {
      withheld.push({ method: write, args });
      return true;
    }
    return (write as any).apply(this, args);
  } as typeof res.write;

  res.end = function (this: http.ServerResponse, ...args: any[]) {
    if (ending) {
      withheld.push({ method: end, args });
      return this;
    }
    writeImplicitHead(this, args);
    if (held.length === 0 && withheld.length === 0) return (end as any).apply(this, args);
    ending = true;
    withheld.push({ method: end, args });
    void settleWithin(held, capMs)
      .catch((err: unknown) => {
        sendControl("log", { level: "error", message: String((err as Error)?.stack || err) });
      })
      .finally(replay);
    return this;
  } as typeof res.end;
}

function wrapWithOcelContext(invoke: Invoke, trust: Trust): http.RequestListener {
  const finishCapMs = finishBeforeResponseMs(process.env);
  return (req, res) => {
    const requestId = req.headers["x-ocel-request-id"];
    const admitted = !trust.guard || trust.guard(req.headers);
    normalizeLoopbackHeaders(req.headers, trust);
    const start = performance.now();

    const pending: Promise<unknown>[] = [];
    const held: Promise<unknown>[] = [];
    const waitUntil = (p: Promise<unknown>): void => {
      pending.push(Promise.resolve(p));
    };
    const holdEnd = (p: Promise<unknown>): void => {
      waitUntil(p);
      if (finishCapMs > 0) held.push(Promise.resolve(p));
    };
    if (finishCapMs > 0) holdLastByte(res, held, finishCapMs);

    let finalized = false;
    const finalize = (): void => {
      if (finalized) return;
      finalized = true;
      if (!req.readableEnded) req.resume();
      sendControl("request-end", {
        requestId,
        status: res.statusCode,
        durationMs: performance.now() - start,
      });
      void drainWaitUntil(pending).then(() => {
        sendControl("invocation-complete", { requestId });
      });
    };
    res.once("finish", finalize);
    res.once("close", finalize);

    Promise.resolve()
      .then(() => {
        if (!admitted) {
          req.resume();
          res.writeHead(403);
          res.end();
          return;
        }
        return invoke(req, res, { waitUntil, holdEnd });
      })
      .catch((err: any) => {
        sendControl("log", { level: "error", message: String(err?.stack || err) });
        if (!res.headersSent) res.writeHead(500);
        res.end("Internal Server Error");
      });
  };
}

export function serveInvoke(invoke: Invoke, onListening?: OnListening, bind?: Bind): Promise<void> {
  return startServer(
    http.createServer(
      wrapWithOcelContext(invoke, { forwarded: true, guard: originGuard(process.env) }),
    ),
    onListening,
    true,
    bind,
  );
}

export function serveEntry(invoke: Invoke, bind?: Bind): Promise<void> {
  return startServer(
    http.createServer(wrapWithOcelContext(invoke, { guard: originGuard(process.env) })),
    undefined,
    true,
    bind,
  );
}

export function serveLocal(invoke: Invoke): Promise<number> {
  return listen(
    http.createServer(wrapWithOcelContext(invoke, { entry: true, forwarded: true })),
    loopback,
  );
}

export function serveServer(
  server: http.Server,
  onListening?: OnListening,
  bind?: Bind,
): Promise<void> {
  const lifted = server.listeners("request") as http.RequestListener[];
  server.removeAllListeners("request");

  const invoke: Invoke = (req, res) => {
    for (const listener of [...lifted]) listener.call(server, req, res);
  };
  server.on(
    "request",
    wrapWithOcelContext(invoke, { forwarded: true, guard: originGuard(process.env) }),
  );

  type Lifted = http.RequestListener & { listener?: http.RequestListener };

  const drop = (listener: http.RequestListener): void => {
    for (let i = lifted.length - 1; i >= 0; i--) {
      const entry = lifted[i] as Lifted;
      if (entry === listener || entry.listener === listener) {
        lifted.splice(i, 1);
        return;
      }
    }
  };

  const onceWrapper = (listener: http.RequestListener): Lifted => {
    const wrapper: Lifted = function (this: unknown, req, res) {
      drop(wrapper);
      listener.call(this, req, res);
    };
    wrapper.listener = listener;
    return wrapper;
  };

  const realOn = server.on.bind(server);
  const realOnce = server.once.bind(server);
  const realPrependListener = server.prependListener.bind(server);
  const realPrependOnceListener = server.prependOnceListener.bind(server);
  const realRemoveListener = server.removeListener.bind(server);

  const add = (
    listener: http.RequestListener,
    opts: { once?: boolean; prepend?: boolean },
  ): http.Server => {
    const entry = opts.once ? onceWrapper(listener) : listener;
    if (opts.prepend) lifted.unshift(entry);
    else lifted.push(entry);
    return server;
  };

  type Listener = (...args: any[]) => void;

  server.on = ((event: string, listener: Listener) =>
    event === "request"
      ? add(listener as http.RequestListener, {})
      : realOn(event, listener)) as typeof server.on;
  server.addListener = server.on as typeof server.addListener;

  server.once = ((event: string, listener: Listener) =>
    event === "request"
      ? add(listener as http.RequestListener, { once: true })
      : realOnce(event, listener)) as typeof server.once;

  server.prependListener = ((event: string, listener: Listener) =>
    event === "request"
      ? add(listener as http.RequestListener, { prepend: true })
      : realPrependListener(event, listener)) as typeof server.prependListener;

  server.prependOnceListener = ((event: string, listener: Listener) =>
    event === "request"
      ? add(listener as http.RequestListener, { once: true, prepend: true })
      : realPrependOnceListener(event, listener)) as typeof server.prependOnceListener;

  server.removeListener = ((event: string, listener: Listener) => {
    if (event !== "request") return realRemoveListener(event, listener);
    drop(listener as http.RequestListener);
    return server;
  }) as typeof server.removeListener;
  server.off = server.removeListener as typeof server.off;

  return startServer(server, onListening, true, bind);
}

export type OnListening = (port: number) => void;

export interface Bind {
  host: string;
  port: number;
}

const loopback: Bind = { host: "127.0.0.1", port: 0 };

const everyNetwork = "0.0.0.0";

export function readPortBind(env: NodeJS.ProcessEnv): Bind {
  const declared = env.PORT;
  if (!declared) {
    throw new Error("ocel: nothing set PORT, so there is no port to serve this function on");
  }
  const port = Number(declared);
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(`ocel: PORT is ${declared}, which is not a port to serve this function on`);
  }
  return { host: everyNetwork, port };
}

function listen(server: http.Server, bind: Bind): Promise<number> {
  server.keepAliveTimeout = 0;
  server.headersTimeout = 0;
  return new Promise((resolve, reject) => {
    server.on("error", reject);
    server.listen(bind, () => {
      const addr = server.address();
      if (!addr || typeof addr === "string") {
        reject(new Error(`unexpected server.address(): ${JSON.stringify(addr)}`));
        return;
      }
      resolve(addr.port);
    });
  });
}

export async function startServer(
  server: http.Server,
  onListening?: OnListening,
  lifecycle = false,
  bind: Bind = loopback,
): Promise<void> {
  const port = await listen(server, bind);
  onListening?.(port);
  sendControl("server-ready", { httpPort: port, lifecycle });
}

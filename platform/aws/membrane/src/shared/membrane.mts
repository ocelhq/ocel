import net from "node:net";
import http from "node:http";
import Module from "node:module";
import { createHash, timingSafeEqual } from "node:crypto";

let controlSocket: net.Socket | null = null;
const controlHandlers = new Set<(message: unknown) => void>();

function control(): net.Socket {
  if (!controlSocket) {
    controlSocket = net.createConnection(process.env.OCEL_CONTROL_SOCKET!);
    receive(controlSocket);
  }
  return controlSocket;
}

export function sendControl(type: string, payload: unknown): void {
  control().write(JSON.stringify({ type, payload }) + "\n");
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
    sendControl(
      "compile-cache-warmed",
      warmNow(warm, (message as { payload?: unknown }).payload),
    );
  });
}

export interface OcelContext {
  waitUntil: (p: Promise<unknown>) => void;
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

const cloudflareEdgeKind = "cloudflare";

const cloudfrontEdgeKind = "cloudfront";

export function routerMode(edgeKind: string | undefined): boolean {
  return edgeKind !== undefined && edgeKind !== "" && edgeKind !== cloudflareEdgeKind;
}

export function invalidatesByCacheTag(edgeKind: string | undefined): boolean {
  return edgeKind === cloudfrontEdgeKind;
}

const originSecretVar = "OCEL_ORIGIN_SECRET";

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
  delete env[originSecretVar];
  if (!routerMode(env.OCEL_EDGE_KIND) || env[originSignedVar]) return undefined;
  if (!secret) return () => false;
  const expected = digest(secret);
  return (headers) => timingSafeEqual(digest(presentedSecret(headers)), expected);
}

interface Trust {
  entry?: boolean;
  forwarded?: boolean;
  guard?: OriginGuard;
}

function normalizeLoopbackHeaders(
  headers: http.IncomingHttpHeaders,
  trust: Trust,
): void {
  if (trust.forwarded) {
    const forwarded = String(headers["x-forwarded-host"] ?? "").split(",")[0]?.trim();
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

function wrapWithOcelContext(invoke: Invoke, trust: Trust): http.RequestListener {
  return (req, res) => {
    const requestId = req.headers["x-ocel-request-id"];
    const admitted = !trust.guard || trust.guard(req.headers);
    normalizeLoopbackHeaders(req.headers, trust);
    const start = performance.now();

    const pending: Promise<unknown>[] = [];
    const waitUntil = (p: Promise<unknown>): void => {
      pending.push(Promise.resolve(p));
    };

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
        return invoke(req, res, { waitUntil });
      })
      .catch((err: any) => {
        sendControl("log", { level: "error", message: String(err?.stack || err) });
        if (!res.headersSent) res.writeHead(500);
        res.end("Internal Server Error");
      });
  };
}

export function serveInvoke(invoke: Invoke, onListening?: OnListening): Promise<void> {
  return startServer(
    http.createServer(
      wrapWithOcelContext(invoke, { forwarded: true, guard: originGuard(process.env) }),
    ),
    onListening,
    true,
  );
}

export function serveEntry(invoke: Invoke): Promise<void> {
  return startServer(
    http.createServer(wrapWithOcelContext(invoke, { guard: originGuard(process.env) })),
    undefined,
    true,
  );
}

export function serveLocal(invoke: Invoke): Promise<number> {
  return listen(http.createServer(wrapWithOcelContext(invoke, { entry: true, forwarded: true })));
}

export function serveServer(server: http.Server, onListening?: OnListening): Promise<void> {
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

  return startServer(server, onListening, true);
}

export type OnListening = (port: number) => void;

function listen(server: http.Server): Promise<number> {
  server.keepAliveTimeout = 0;
  server.headersTimeout = 0;
  return new Promise((resolve, reject) => {
    server.on("error", reject);
    server.listen({ host: "127.0.0.1", port: 0 }, () => {
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
): Promise<void> {
  const port = await listen(server);
  onListening?.(port);
  sendControl("server-ready", { httpPort: port, lifecycle });
}

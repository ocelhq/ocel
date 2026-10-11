import { createHash, timingSafeEqual } from "node:crypto";
import http from "node:http";
import Module from "node:module";
import net from "node:net";
import type { Duplex } from "node:stream";

let controlSocket: net.Socket | null = null;
const controlHandlers = new Set<(message: unknown) => void>();

export function hasControl(): boolean {
  return Boolean(process.env.OCEL_CONTROL_SOCKET);
}

function control(): net.Socket | null {
  if (!hasControl()) return null;
  if (!controlSocket) {
    controlSocket = net.createConnection(process.env.OCEL_CONTROL_SOCKET!);
    controlSocket.on("error", (err) => {
      console.error(
        `ocel runtime: the control socket failed, so the host hears no more from it: ${err.message}`,
      );
    });
    receive(controlSocket);
  }
  return controlSocket;
}

export function sendControl(type: string, payload: unknown): void {
  const socket = control();
  if (socket && !socket.destroyed) socket.write(`${JSON.stringify({ type, payload })}\n`);
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

export function invalidatesByCacheTag(env: NodeJS.ProcessEnv): boolean {
  return Boolean(env[cacheTagPurgeVar]);
}

const originFreshOnlyVar = "OCEL_ORIGIN_FRESH_ONLY";

export function servesOnlyFresh(env: NodeJS.ProcessEnv): boolean {
  return Boolean(env[originFreshOnlyVar]);
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

const previewGateVar = "OCEL_PREVIEW_GATE";
const previewGateAtEdgeVar = "OCEL_PREVIEW_GATE_AT_EDGE";

function takeOriginGate(env: NodeJS.ProcessEnv): Pick<Trust, "gate" | "gateAtEdge"> {
  const gate = env[previewGateVar];
  const gateAtEdge = env[previewGateAtEdgeVar] === "true";
  delete env[previewGateVar];
  delete env[previewGateAtEdgeVar];
  return { gate, gateAtEdge };
}

interface Trust {
  entry?: boolean;
  forwarded?: boolean;
  guard?: OriginGuard;
  gate?: string;
  gateAtEdge?: boolean;
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

const draining = new Set<Promise<void>>();

let finishesOnTerm = false;

function finishDrainingOnTerm(): void {
  if (finishesOnTerm) return;
  finishesOnTerm = true;
  process.once("SIGTERM", () => {
    void Promise.allSettled([...draining]).then(() => process.kill(process.pid, "SIGTERM"));
  });
}

function wrapWithOcelContext(invoke: Invoke, trust: Trust): http.RequestListener {
  finishDrainingOnTerm();
  return (req, res) => {
    const requestId = req.headers["x-ocel-request-id"];
    const admitted = !trust.guard || trust.guard(req.headers);
    const reachedHost = req.headers.host ?? "";
    normalizeLoopbackHeaders(req.headers, trust);
    const start = performance.now();

    const pending: Promise<unknown>[] = [];
    const waitUntil = (p: Promise<unknown>): void => {
      pending.push(Promise.resolve(p));
    };

    let complete!: () => void;
    const completed = new Promise<void>((resolve) => {
      complete = resolve;
    });
    draining.add(completed);
    void completed.then(() => draining.delete(completed));

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
      void drainWaitUntil(pending)
        .then(() => {
          sendControl("invocation-complete", { requestId });
        })
        .finally(complete);
    };
    res.once("finish", finalize);
    res.once("close", finalize);

    Promise.resolve()
      .then(async () => {
        if (!admitted) {
          req.resume();
          res.writeHead(403);
          res.end();
          return;
        }
        if (trust.gate !== undefined) {
          const { admitPreview } = await import("./preview-gate.mjs");
          if (!(await admitPreview(trust.gate, req, reachedHost, res, trust.gateAtEdge))) return;
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

function originTrust(env: NodeJS.ProcessEnv, forwarded: boolean): Trust {
  return { forwarded, guard: originGuard(env), ...takeOriginGate(env) };
}

export function serveInvoke(invoke: Invoke, onListening?: OnListening, bind?: Bind): Promise<void> {
  return startServer(
    http.createServer(wrapWithOcelContext(invoke, originTrust(process.env, true))),
    onListening,
    true,
    bind,
  );
}

export function serveEntry(invoke: Invoke, bind?: Bind): Promise<void> {
  return startServer(
    http.createServer(wrapWithOcelContext(invoke, originTrust(process.env, false))),
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

type Listener = (...args: any[]) => void;

type UpgradeListener = (req: http.IncomingMessage, socket: Duplex, head: Buffer) => void;

const forbiddenUpgradeAnswer =
  "HTTP/1.1 403 Forbidden\r\nconnection: close\r\ncontent-length: 0\r\n\r\n";

function wrapUpgrade(server: http.Server, lifted: Listener[], trust: Trust): UpgradeListener {
  return (req, socket, head) => {
    const admitted = !trust.guard || trust.guard(req.headers);
    const reachedHost = req.headers.host ?? "";
    normalizeLoopbackHeaders(req.headers, trust);
    Promise.resolve()
      .then(async () => {
        if (!admitted) {
          socket.end(forbiddenUpgradeAnswer);
          return;
        }
        if (trust.gate !== undefined) {
          const { admitPreviewUpgrade } = await import("./preview-gate.mjs");
          if (!(await admitPreviewUpgrade(trust.gate, req, reachedHost, socket, trust.gateAtEdge)))
            return;
        }
        for (const listener of [...lifted]) listener.call(server, req, socket, head);
      })
      .catch((err: any) => {
        sendControl("log", { level: "error", message: String(err?.stack || err) });
        socket.destroy();
      });
  };
}

export function serveServer(
  server: http.Server,
  onListening?: OnListening,
  bind?: Bind,
): Promise<void> {
  type Lifted = Listener & { listener?: Listener };

  const lifted: Record<string, Lifted[]> = {};
  for (const event of ["request", "upgrade"]) {
    lifted[event] = server.listeners(event) as Lifted[];
    server.removeAllListeners(event);
  }
  const isLifted = (event: string | symbol): event is "request" | "upgrade" =>
    event === "request" || event === "upgrade";

  const trust = originTrust(process.env, true);
  const invoke: Invoke = (req, res) => {
    for (const listener of [...lifted.request!]) listener.call(server, req, res);
  };
  server.on("request", wrapWithOcelContext(invoke, trust));

  const realOn = server.on.bind(server);
  const realOnce = server.once.bind(server);
  const realPrependListener = server.prependListener.bind(server);
  const realPrependOnceListener = server.prependOnceListener.bind(server);
  const realRemoveListener = server.removeListener.bind(server);

  const upgradeDoor = wrapUpgrade(server, lifted.upgrade!, trust);
  let upgradeDoorOpen = false;
  const matchUpgradeDoor = (): void => {
    const wanted = lifted.upgrade!.length > 0;
    if (wanted === upgradeDoorOpen) return;
    upgradeDoorOpen = wanted;
    if (wanted) realOn("upgrade", upgradeDoor);
    else realRemoveListener("upgrade", upgradeDoor);
  };
  matchUpgradeDoor();

  const drop = (event: "request" | "upgrade", listener: Listener): void => {
    const listeners = lifted[event]!;
    for (let i = listeners.length - 1; i >= 0; i--) {
      const entry = listeners[i]!;
      if (entry === listener || entry.listener === listener) {
        listeners.splice(i, 1);
        break;
      }
    }
    matchUpgradeDoor();
  };

  const onceWrapper = (event: "request" | "upgrade", listener: Listener): Lifted => {
    const wrapper: Lifted = function (this: unknown, ...args: any[]) {
      drop(event, wrapper);
      listener.apply(this, args);
    };
    wrapper.listener = listener;
    return wrapper;
  };

  const add = (
    event: "request" | "upgrade",
    listener: Listener,
    opts: { once?: boolean; prepend?: boolean },
  ): http.Server => {
    const entry = opts.once ? onceWrapper(event, listener) : listener;
    if (opts.prepend) lifted[event]!.unshift(entry);
    else lifted[event]!.push(entry);
    matchUpgradeDoor();
    return server;
  };

  server.on = ((event: string, listener: Listener) =>
    isLifted(event) ? add(event, listener, {}) : realOn(event, listener)) as typeof server.on;
  server.addListener = server.on as typeof server.addListener;

  server.once = ((event: string, listener: Listener) =>
    isLifted(event)
      ? add(event, listener, { once: true })
      : realOnce(event, listener)) as typeof server.once;

  server.prependListener = ((event: string, listener: Listener) =>
    isLifted(event)
      ? add(event, listener, { prepend: true })
      : realPrependListener(event, listener)) as typeof server.prependListener;

  server.prependOnceListener = ((event: string, listener: Listener) =>
    isLifted(event)
      ? add(event, listener, { once: true, prepend: true })
      : realPrependOnceListener(event, listener)) as typeof server.prependOnceListener;

  server.removeListener = ((event: string, listener: Listener) => {
    if (!isLifted(event)) return realRemoveListener(event, listener);
    drop(event, listener);
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

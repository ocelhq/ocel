import net from "node:net";
import http from "node:http";
import Module from "node:module";

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

function normalizeLoopbackHeaders(headers: http.IncomingHttpHeaders): void {
  const forwarded = String(headers["x-forwarded-host"] ?? "").split(",")[0]?.trim();
  if (forwarded) headers.host = forwarded;
  if (headers["x-ocel-request-id"] === undefined) delete headers["x-ocel-entry"];
  delete headers["x-ocel-request-id"];
  delete headers["x-ocel-trace-id"];
}

function wrapWithOcelContext(invoke: Invoke): http.RequestListener {
  return (req, res) => {
    const requestId = req.headers["x-ocel-request-id"];
    normalizeLoopbackHeaders(req.headers);
    const start = performance.now();

    const pending: Promise<unknown>[] = [];
    const waitUntil = (p: Promise<unknown>): void => {
      pending.push(Promise.resolve(p));
    };

    let finalized = false;
    const finalize = (): void => {
      if (finalized) return;
      finalized = true;
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
      .then(() => invoke(req, res, { waitUntil }))
      .catch((err: any) => {
        sendControl("log", { level: "error", message: String(err?.stack || err) });
        if (!res.headersSent) res.writeHead(500);
        res.end("Internal Server Error");
      });
  };
}

export function serveInvoke(invoke: Invoke, onListening?: OnListening): Promise<void> {
  return startServer(http.createServer(wrapWithOcelContext(invoke)), onListening);
}

export type OnListening = (port: number) => void;

export function startServer(server: http.Server, onListening?: OnListening): Promise<void> {
  return new Promise((resolve, reject) => {
    server.on("error", reject);
    server.listen({ host: "127.0.0.1", port: 0 }, () => {
      const addr = server.address();
      if (!addr || typeof addr === "string") {
        reject(new Error(`unexpected server.address(): ${JSON.stringify(addr)}`));
        return;
      }
      onListening?.(addr.port);
      sendControl("server-ready", { httpPort: addr.port });
      resolve();
    });
  });
}

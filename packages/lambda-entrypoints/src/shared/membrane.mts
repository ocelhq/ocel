import net from "node:net";
import http from "node:http";

let controlSocket: net.Socket | null = null;

// The control socket is opened lazily on first use so importing this module has
// no side effect (tests can load it without a live socket).
function control(): net.Socket {
  if (!controlSocket) {
    controlSocket = net.createConnection(process.env.OCEL_CONTROL_SOCKET!);
  }
  return controlSocket;
}

export function sendControl(type: string, payload: unknown): void {
  control().write(JSON.stringify({ type, payload }) + "\n");
}

// Reports an error that killed the boot, before there is an app to serve or,
// often, a control socket to talk over. It must not go through sendControl:
// that write is async, and the process.exit that follows a fatal error
// abandons it, losing the one message explaining why the app never came up.
// stderr is the real fd inherited from the Go bootstrap, which forwards it to
// CloudWatch, so the write lands before the process goes.
export function reportFatalBoot(err: unknown): void {
  const detail = err instanceof Error ? (err.stack ?? err.message) : String(err);
  console.error(`ocel: fatal boot error: ${detail}`);
}

export interface OcelContext {
  waitUntil: (p: Promise<unknown>) => void;
}

export type Invoke = (
  req: http.IncomingMessage,
  res: http.ServerResponse,
  ocel: OcelContext,
) => void | Promise<void>;

// drainWaitUntil settles every registered background promise — including any
// registered while an earlier one was still settling — then resolves. Rejections
// are logged and swallowed so one failed task can't sink the rest or fail the
// invocation. The pending array is drained in place.
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

function wrapWithOcelContext(invoke: Invoke): http.RequestListener {
  return (req, res) => {
    const requestId = req.headers["x-ocel-request-id"];
    delete req.headers["x-ocel-request-id"];
    delete req.headers["x-ocel-trace-id"];
    const start = performance.now();

    const pending: Promise<unknown>[] = [];
    const waitUntil = (p: Promise<unknown>): void => {
      pending.push(Promise.resolve(p));
    };

    // Fire once, on whichever of finish/close comes first (an aborted request
    // emits close without finish). Report per-request telemetry, then hold the
    // invocation open — via the control socket — until every waitUntil promise
    // settles, so the runtime doesn't freeze the sandbox out from under them.
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

export function serveInvoke(invoke: Invoke): Promise<void> {
  return startServer(http.createServer(wrapWithOcelContext(invoke)));
}

export function startServer(server: http.Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.on("error", reject);
    server.listen({ host: "127.0.0.1", port: 0 }, () => {
      const addr = server.address();
      if (!addr || typeof addr === "string") {
        reject(new Error(`unexpected server.address(): ${JSON.stringify(addr)}`));
        return;
      }
      sendControl("server-ready", { httpPort: addr.port });
      resolve();
    });
  });
}

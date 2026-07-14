import net from "node:net";
import http from "node:http";

const controlSocket = net.createConnection(process.env.OCEL_CONTROL_SOCKET!);

export function sendControl(type: string, payload: unknown): void {
  controlSocket.write(JSON.stringify({ type, payload }) + "\n");
}

export type Invoke = (
  req: http.IncomingMessage,
  res: http.ServerResponse,
) => void | Promise<void>;

function wrapWithOcelContext(invoke: Invoke): http.RequestListener {
  return (req, res) => {
    const requestId = req.headers["x-ocel-request-id"];
    delete req.headers["x-ocel-request-id"];
    delete req.headers["x-ocel-trace-id"];
    const start = performance.now();
    res.once("finish", () => {
      sendControl("request-end", {
        requestId,
        status: res.statusCode,
        durationMs: performance.now() - start,
      });
    });
    Promise.resolve()
      .then(() => invoke(req, res))
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

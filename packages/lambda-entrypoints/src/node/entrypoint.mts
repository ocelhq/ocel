import net from "node:net";
import http from "node:http";
import { isAbsolute } from "node:path";
import { pathToFileURL } from "node:url";

const controlSocket = net.createConnection(process.env.OCEL_CONTROL_SOCKET!);
function sendControl(type: string, payload: unknown): void {
  controlSocket.write(JSON.stringify({ type, payload }) + "\n");
}

type Loaded =
  | { kind: "server"; value: http.Server }
  | { kind: "export"; value: unknown };

async function loadUserApp(entrypoint: string): Promise<Loaded> {
  const href = isAbsolute(entrypoint) ? pathToFileURL(entrypoint).href : entrypoint;

  const listenHook = interceptListen();

  const importPromise: Promise<Loaded> = import(href).then((mod) => {
    let exported: any = mod;
    for (let i = 0; i < 5; i++) {
      if (exported.default) exported = exported.default;
    }
    return { kind: "export", value: exported };
  });

  const serverPromise: Promise<Loaded> = listenHook
    .waitForServer()
    .then((server) => ({ kind: "server", value: server }));

  const result = await Promise.race([
    serverPromise,
    importPromise.then((r) => {
      // Prefer the export if it's itself a server/app; otherwise keep waiting
      // for a .listen() capture (Nest resolves via serverPromise).
      const v = r.value as any;
      if (v && (typeof v === "function" || typeof v.listen === "function")) {
        return r;
      }
      return serverPromise;
    }),
  ]);

  listenHook.restore();
  return result;
}

type NodeHandler = (req: http.IncomingMessage, res: http.ServerResponse) => void;
type FetchHandler = (request: Request) => Response | Promise<Response>;

type Resolved =
  | { type: "server"; server: http.Server }
  | { type: "node-handler"; handler: NodeHandler }
  | { type: "web-handler"; fetch: FetchHandler };

function resolveHandler(exported: any): Resolved {
  // Callability MUST be checked before `.listen`: an Express `app` is both a
  // function and has `.listen`, so a `.listen`-first check would route it to
  // the "server" branch, and app.address() (nonexistent) would later throw.
  // Treating it as a node-handler makes us wrap it in http.createServer.
  if (typeof exported === "function") {
    return { type: "node-handler", handler: exported };
  }
  if (exported && typeof exported.listen === "function") {
    return { type: "server", server: exported };
  }
  if (exported && typeof exported.fetch === "function") {
    return { type: "web-handler", fetch: exported.fetch };
  }
  const methods = ["GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"];
  if (exported && methods.some((m) => typeof exported[m] === "function")) {
    return { type: "web-handler", fetch: dispatchByMethod(exported) };
  }
  throw new Error(
    "Default export must be an Express app, a (req,res) handler, or a fetch handler.",
  );
}

function buildServer(resolved: Resolved): http.Server {
  if (resolved.type === "server") {
    return resolved.server;
  }
  const requestListener =
    resolved.type === "node-handler"
      ? wrapWithOcelContext(resolved.handler)
      : wrapWithOcelContext(fetchToNodeHandler(resolved.fetch));
  return http.createServer(requestListener);
}

function wrapWithOcelContext(handler: NodeHandler): http.RequestListener {
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
    try {
      handler(req, res);
    } catch (err: any) {
      sendControl("log", { level: "error", message: String(err?.stack || err) });
      if (!res.headersSent) res.writeHead(500);
      res.end("Internal Server Error");
    }
  };
}

function fetchToNodeHandler(fetchFn: FetchHandler): NodeHandler {
  return async (req, res) => {
    const url = `http://${req.headers.host || "localhost"}${req.url}`;
    const body = req.method === "GET" || req.method === "HEAD" ? null : req;
    const request = new Request(url, {
      method: req.method,
      headers: req.headers as any,
      body: body as any,
      duplex: "half",
    } as RequestInit);
    const response = await fetchFn(request);
    res.writeHead(response.status, Object.fromEntries(response.headers));
    if (response.body) {
      const reader = response.body.getReader();
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        res.write(value);
      }
    }
    res.end();
  };
}

function dispatchByMethod(exported: any): FetchHandler {
  return (request) => {
    const fn = exported[request.method];
    if (typeof fn !== "function") return new Response(null, { status: 405 });
    return fn(request);
  };
}

interface ListenHook {
  waitForServer: () => Promise<http.Server>;
  restore: () => void;
}

function interceptListen(): ListenHook {
  const realListen = http.Server.prototype.listen;
  let captured: http.Server | null = null;
  const waiters: Array<(server: http.Server) => void> = [];

  http.Server.prototype.listen = function (this: http.Server, ...args: any[]) {
    // Restore immediately so our own later listen() binds for real; the user's
    // .listen() is captured but never actually binds their port.
    http.Server.prototype.listen = realListen;
    captured = this;
    const cb = args.find((a) => typeof a === "function");
    if (cb) setImmediate(cb);
    waiters.forEach((w) => w(this));
    return this;
  } as typeof http.Server.prototype.listen;

  return {
    waitForServer: () =>
      new Promise((resolve) => {
        if (captured) resolve(captured);
        else waiters.push(resolve);
      }),
    restore: () => {
      http.Server.prototype.listen = realListen;
    },
  };
}

async function boot(): Promise<void> {
  const loaded = await loadUserApp(process.env.OCEL_HANDLER!);

  let server: http.Server;
  if (loaded.kind === "server") {
    server = loaded.value;
  } else {
    server = buildServer(resolveHandler(loaded.value));
  }

  await new Promise<void>((resolve, reject) => {
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

boot().catch((err) => {
  sendControl("log", { level: "error", message: String(err?.stack || err) });
  process.exit(1);
});

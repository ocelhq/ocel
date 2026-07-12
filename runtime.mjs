// runtime.mjs
import net from "node:net";
import http from "node:http";
import { isAbsolute } from "node:path";
import { pathToFileURL } from "node:url";

var controlSocket = net.createConnection(process.env.OCEL_CONTROL_SOCKET);
function sendControl(type, payload) {
  controlSocket.write(JSON.stringify({ type, payload }) + "\n");
}

async function loadUserApp(entrypoint) {
  const href = isAbsolute(entrypoint) ? pathToFileURL(entrypoint).href : entrypoint;
  let mod = await import(href);
  let exported = mod;
  for (let i = 0; i < 5; i++) {
    if (exported.default) exported = exported.default;
  }
  return exported;
}

function resolveHandler(exported) {
  // Callable check MUST come first. An Express `app` is a function AND has a
  // `.listen` method — if we checked `.listen` first, Express would wrongly
  // match the "server" branch, we'd return the app as-is, and app.address()
  // (which doesn't exist) would throw. Checking callability first routes
  // Express to node-handler, so WE wrap it in http.createServer ourselves.
  if (typeof exported === "function") {
    return { type: "node-handler", handler: exported };
  }
  // Only a genuine, non-callable http.Server instance reaches here.
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
    "Default export must be an Express app, a (req,res) handler, or a fetch handler."
  );
}

function buildServer(resolved) {
  let requestListener;
  if (resolved.type === "server") {
    return resolved.server;
  }
  if (resolved.type === "node-handler") {
    requestListener = wrapWithOcelContext(resolved.handler);
  }
  if (resolved.type === "web-handler") {
    requestListener = wrapWithOcelContext(fetchToNodeHandler(resolved.fetch));
  }
  return http.createServer(requestListener);
}

function wrapWithOcelContext(handler) {
  return (req, res) => {
    const requestId = req.headers["x-ocel-request-id"];
    const traceId = req.headers["x-ocel-trace-id"];
    delete req.headers["x-ocel-request-id"];
    delete req.headers["x-ocel-trace-id"];
    const start = performance.now();
    res.once("finish", () => {
      sendControl("request-end", {
        requestId,
        status: res.statusCode,
        durationMs: performance.now() - start
      });
    });
    try {
      handler(req, res);
    } catch (err) {
      sendControl("log", {
        level: "error",
        message: String(err?.stack || err)
      });
      if (!res.headersSent) res.writeHead(500);
      res.end("Internal Server Error");
    }
  };
}

function fetchToNodeHandler(fetchFn) {
  return async (req, res) => {
    const url = `http://${req.headers.host || "localhost"}${req.url}`;
    const body = ["GET", "HEAD"].includes(req.method) ? null : req;
    const request = new Request(url, {
      method: req.method,
      headers: req.headers,
      body,
      duplex: "half"
    });
    const response = await fetchFn(request);
    res.writeHead(response.status, Object.fromEntries(response.headers));
    if (response.body) {
      const reader = response.body.getReader();
      for (; ; ) {
        const { done, value } = await reader.read();
        if (done) break;
        res.write(value);
      }
    }
    res.end();
  };
}

function dispatchByMethod(exported) {
  return (request) => {
    const fn = exported[request.method];
    if (typeof fn !== "function") return new Response(null, { status: 405 });
    return fn(request);
  };
}

async function boot() {
  const exported = await loadUserApp(process.env.OCEL_HANDLER);
  const resolved = resolveHandler(exported);
  const server = buildServer(resolved);
  await new Promise((resolve, reject) => {
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
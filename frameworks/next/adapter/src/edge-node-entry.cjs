const { AsyncLocalStorage } = require("node:async_hooks");
const { readFileSync } = require("node:fs");
const path = require("node:path");
const { Readable } = require("node:stream");
const { pipeline } = require("node:stream/promises");

let assetPaths = null;

function installAssetFetch() {
  if (assetPaths) return;
  assetPaths = new Map();
  const inner = globalThis.fetch;
  globalThis.fetch = (input, init) => {
    const url =
      typeof input === "string"
        ? input
        : input instanceof URL
          ? input.href
          : input?.url;
    const file = assetFile(url);
    if (file === undefined) return inner(input, init);
    return Promise.resolve(new Response(readFileSync(file)));
  };
}

function assetFile(url) {
  if (typeof url !== "string" || !url.startsWith("blob:")) return undefined;
  const name = url.slice(5);
  if (assetPaths.has(name)) return assetPaths.get(name);
  let decoded;
  try {
    decoded = decodeURIComponent(name);
  } catch {
    return undefined;
  }
  return assetPaths.get(decoded);
}

function requestUrl(req) {
  const host = req.headers["x-forwarded-host"] || req.headers.host || "localhost";
  const proto = req.headers["x-forwarded-proto"] || "https";
  return `${String(proto).split(",")[0].trim()}://${String(host).split(",")[0].trim()}${req.url || "/"}`;
}

function webHeaders(nodeHeaders) {
  const headers = new Headers();
  for (const [name, value] of Object.entries(nodeHeaders)) {
    if (value === undefined) continue;
    if (Array.isArray(value)) for (const one of value) headers.append(name, one);
    else headers.set(name, String(value));
  }
  return headers;
}

function webRequest({ url, method, headers, body, signal }) {
  const hasBody = method !== "GET" && method !== "HEAD";
  return new Request(url, {
    method,
    headers: webHeaders(headers),
    ...(hasBody && body ? { body, duplex: "half" } : {}),
    ...(signal ? { signal } : {}),
  });
}

const DROPPED_RESPONSE_HEADERS = new Set([
  "connection",
  "content-encoding",
  "content-length",
  "date",
  "keep-alive",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
]);

function isDroppedResponseHeader(name) {
  const lower = name.toLowerCase();
  return DROPPED_RESPONSE_HEADERS.has(lower) || lower.startsWith("proxy-");
}

async function writeResponse(response, res) {
  res.statusCode = response.status;
  for (const [name, value] of response.headers) {
    if (name.toLowerCase() === "set-cookie") continue;
    if (isDroppedResponseHeader(name)) continue;
    res.setHeader(name, value);
  }
  const cookies = response.headers.getSetCookie?.() ?? [];
  if (cookies.length > 0) res.setHeader("set-cookie", cookies);
  if (!response.body) {
    res.end();
    return;
  }
  await pipeline(Readable.fromWeb(response.body), res);
}

// TODO(#419): the origin function has no fetch-cache RPC, so a waived edge
// route's cached fetches always miss and its writes are dropped. Until one
// exists, missing beats the throw a compiled-in edge cache handler would raise.
const originCacheBinding = {
  scope: "",
  rpc: {
    fetchGet: async () => null,
    fetchSet: async () => {},
    revalidateTags: async () => {},
  },
};

async function load(dir, spec) {
  globalThis.self ??= globalThis;
  globalThis.AsyncLocalStorage ??= AsyncLocalStorage;
  globalThis.NEXT_CLIENT_ASSET_SUFFIX = spec.clientAssetSuffix ?? "";
  globalThis.__OCEL_EDGE_CACHE ??= originCacheBinding;
  globalThis.__OCEL_EDGE_ENTRY = spec.entryKey;
  for (const [name, value] of Object.entries(spec.env ?? {})) {
    process.env[name] ??= value;
  }

  installAssetFetch();
  for (const [name, rel] of Object.entries(spec.assets ?? {})) {
    assetPaths.set(name, path.join(dir, rel));
  }

  for (const [name, rel] of Object.entries(spec.wasm ?? {})) {
    globalThis[name] ??= new WebAssembly.Module(readFileSync(path.join(dir, rel)));
  }

  for (const rel of spec.chunks) require(path.join(dir, rel));

  const registered = await globalThis._ENTRIES?.[spec.entryKey];
  const handler = registered?.[spec.handlerExport];
  if (typeof handler !== "function") {
    throw new Error(
      `ocel: edge entry ${spec.entryKey} registered no ${spec.handlerExport} export`,
    );
  }
  return handler;
}

function invoke(handler, entryKey, request, waitUntil) {
  globalThis.__OCEL_EDGE_ENTRY = entryKey;
  return handler(request, {
    waitUntil: (promise) => waitUntil?.(promise),
    signal: request.signal,
    requestMeta: {},
  });
}

exports.entry = function entry(dir, spec) {
  const loading = load(dir, spec);

  if (spec.kind === "middleware") {
    return loading.then((handler) => ({
      default: async ({ request }) => {
        const response = await invoke(
          handler,
          spec.entryKey,
          webRequest(request),
          request.waitUntil,
        );
        return { response };
      },
    }));
  }

  return loading.then((handler) => ({
    handler: async (req, res, ctx) => {
      const controller = new AbortController();
      res.once("close", () => {
        if (!res.writableEnded) controller.abort();
      });
      const request = webRequest({
        url: requestUrl(req),
        method: req.method || "GET",
        headers: req.headers,
        body: Readable.toWeb(req),
        signal: controller.signal,
      });
      await writeResponse(
        await invoke(handler, spec.entryKey, request, ctx && ctx.waitUntil),
        res,
      );
    },
  }));
};

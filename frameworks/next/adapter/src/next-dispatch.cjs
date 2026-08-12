const fs = require("node:fs");
const path = require("node:path");
const { Readable } = require("node:stream");
const { pipeline } = require("node:stream/promises");

const TAR_ENTRY_OVERHEAD = 512;

const MIDDLEWARE_ENTRY_KEY = "/_middleware";

const MIDDLEWARE_HEADERS_HEADER = "x-ocel-middleware-headers";

function middlewareRequestUrl(req) {
  const host = req.headers["x-forwarded-host"] || req.headers.host || "localhost";
  const proto = req.headers["x-forwarded-proto"] || "https";
  return `${String(proto).split(",")[0].trim()}://${String(host).split(",")[0].trim()}${req.url || "/"}`;
}

async function writeMiddlewareResponse(response, res) {
  res.statusCode = response.status;
  const names = [];
  for (const [name, value] of response.headers) {
    if (name.toLowerCase() === "set-cookie") continue;
    names.push(name);
    res.setHeader(name, value);
  }
  const cookies = response.headers.getSetCookie?.() ?? [];
  if (cookies.length > 0) {
    names.push("set-cookie");
    res.setHeader("set-cookie", cookies);
  }
  res.setHeader(MIDDLEWARE_HEADERS_HEADER, names.join(","));

  if (!response.body) {
    res.end();
    return;
  }
  await pipeline(Readable.fromWeb(response.body), res);
}

function fail(res, message, detail) {
  if (detail !== undefined) console.error(`ocel: ${message}: ${detail}`);
  res.statusCode = 502;
  res.end(`ocel: ${message}`);
}

async function runMiddleware(moduleOrPromise, req, res, ctx, nextConfig) {
  let module;
  try {
    module =
      moduleOrPromise && typeof moduleOrPromise.then === "function"
        ? await moduleOrPromise
        : moduleOrPromise;
  } catch (error) {
    return fail(
      res,
      `entry ${MIDDLEWARE_ENTRY_KEY} failed to load`,
      error?.stack ?? error,
    );
  }

  const adapterFn = module && (module.default || module);
  if (typeof adapterFn !== "function") {
    return fail(res, `entry ${MIDDLEWARE_ENTRY_KEY} exports no adapter function`);
  }

  const method = req.method || "GET";
  const hasBody = method !== "GET" && method !== "HEAD";
  const controller = new AbortController();
  res.once("close", () => {
    if (!res.writableEnded) controller.abort();
  });

  let result;
  try {
    result = await adapterFn({
      handler: module.proxy || module.middleware || module, // mirrors next-server.js's own resolution order
      request: {
        headers: req.headers,
        method,
        nextConfig,
        url: middlewareRequestUrl(req),
        page: {},
        body: hasBody ? Readable.toWeb(req) : undefined,
        signal: controller.signal,
        waitUntil:
          ctx && typeof ctx.waitUntil === "function"
            ? (p) => ctx.waitUntil(p)
            : undefined,
      },
      page: "middleware",
    });
  } catch (error) {
    return fail(res, "middleware failed", error?.stack ?? error);
  }

  if (ctx && typeof ctx.waitUntil === "function" && result?.waitUntil !== undefined) {
    ctx.waitUntil(Promise.resolve(result.waitUntil));
  }

  if (!(result?.response instanceof Response)) {
    return fail(res, `entry ${MIDDLEWARE_ENTRY_KEY} returned no response`);
  }
  await writeMiddlewareResponse(result.response, res);
}

function compileCache() {
  try {
    const { getCompileCacheDir, flushCompileCache } = require("node:module");
    if (typeof getCompileCacheDir !== "function") return null;
    if (typeof flushCompileCache !== "function") return null;
    const dir = getCompileCacheDir();
    if (typeof dir !== "string" || dir.length === 0) return null;
    return { dir, flush: flushCompileCache };
  } catch {
    return null;
  }
}

function measureCompileCache(dir) {
  let items;
  try {
    items = fs.readdirSync(dir, { withFileTypes: true, recursive: true });
  } catch (error) {
    return error?.code === "ENOENT" ? 0 : null;
  }
  let total = 0;
  for (const item of items) {
    if (!item.isFile()) continue;
    try {
      total += fs.lstatSync(path.join(item.parentPath, item.name)).size + TAR_ENTRY_OVERHEAD;
    } catch (error) {
      if (error?.code !== "ENOENT") return null;
    }
  }
  return total;
}

const MAX_REPORTED_SKIPPED = 20;

const unsupportedWarmReport = (entryCount) => ({
  ok: false,
  state: "unsupported",
  entries: entryCount,
  loaded: 0,
  failures: [],
  stoppedBy: "complete",
  skipped: [],
  skippedCount: 0,
  bytes: 0,
  dir: null,
});

const orUnbounded = (value) => (Number.isFinite(value) ? value : Infinity);

const MAX_MEASURE_STRIDE = 8;

function measureStride(headroom, maxDelta) {
  if (maxDelta <= 0) return 1;
  return Math.max(1, Math.min(MAX_MEASURE_STRIDE, Math.floor(headroom / (2 * maxDelta))));
}

module.exports = function createDispatch({
  entries,
  primary,
  routes = { exact: {}, dynamic: [] },
  nextConfig,
  load,
}) {
  const loaded = new Map();

  const dynamic = routes.dynamic.map(([source, key]) => [new RegExp(source, "i"), key]);

  const entryForPathname = (pathname) => {
    if (Object.prototype.hasOwnProperty.call(routes.exact, pathname)) {
      return routes.exact[pathname];
    }
    for (const [re, key] of dynamic) if (re.test(pathname)) return key;
    return undefined;
  };

  let warnedMissingGetter = false;
  const pinResponseCache = (module) => {
    const routeModule = module?.routeModule;
    if (!routeModule) return;
    if (typeof routeModule.getResponseCache !== "function") {
      if (warnedMissingGetter) return;
      warnedMissingGetter = true;
      console.error(
        "ocel: this Next build's route modules expose no getResponseCache; " +
          "an instance whose first request is a PPR resume will serve it with ISR off",
      );
      return;
    }
    try {
      routeModule.getResponseCache({ headers: {} });
    } catch (error) {
      console.error(`ocel: response cache pin failed: ${error?.stack ?? error}`);
    }
  };

  const loadEntry = (key, { memoizeFailure = true } = {}) => {
    const already = loaded.get(key);
    if (already) return already;
    try {
      const moduleOrPromise = load(entries[key]);
      if (moduleOrPromise && typeof moduleOrPromise.then === "function") {
        moduleOrPromise.catch(() => {});
      }
      const entry = { module: moduleOrPromise };
      pinResponseCache(entry.module);
      loaded.set(key, entry);
      return entry;
    } catch (error) {
      console.error(`ocel: entry ${key} failed to load: ${error?.stack ?? error}`);
      if (memoizeFailure) loaded.set(key, { error });
      return { error };
    }
  };

  if (primary) loadEntry(primary);
  if (Object.prototype.hasOwnProperty.call(entries, MIDDLEWARE_ENTRY_KEY)) {
    loadEntry(MIDDLEWARE_ENTRY_KEY);
  }

  return {
    handler(req, res, ctx) {
      const pathname = (req.url || "/").split("?")[0];
      const key =
        typeof req.headers["x-ocel-entry"] === "string"
          ? req.headers["x-ocel-entry"]
          : entryForPathname(pathname);
      if (typeof key !== "string") {
        return fail(res, `no entry serves ${pathname} in this bundle`);
      }
      if (!Object.prototype.hasOwnProperty.call(entries, key)) {
        return fail(res, `bundle carries no entry ${key}`);
      }
      const entry = loadEntry(key);
      if (entry.error) {
        return fail(
          res,
          `entry ${key} failed to load: ${entry.error?.message ?? entry.error}`,
        );
      }
      if (key === MIDDLEWARE_ENTRY_KEY) {
        return runMiddleware(entry.module, req, res, ctx, nextConfig);
      }
      const handler = entry.module?.handler;
      if (typeof handler !== "function") {
        return fail(res, `entry ${key} exports no handler function`);
      }
      return handler(req, res, ctx);
    },

    warm({ deadlineMs, ceilingBytes } = {}) {
      const keys = Object.keys(entries);
      const cache = compileCache();
      if (!cache) return unsupportedWarmReport(keys.length);

      const deadline = orUnbounded(deadlineMs);
      const ceiling = orUnbounded(ceilingBytes);
      const failures = [];
      let count = 0;
      const record = (key, entry) => {
        if (!entry.error) {
          count += 1;
          return;
        }
        failures.push({
          entry: key,
          message: entry.error?.message ?? String(entry.error),
        });
      };

      let bytes = 0;
      let maxDelta = 0;
      let measurable = true;

      const remeasure = ({ isFloor = false } = {}) => {
        cache.flush();
        const measured = measureCompileCache(cache.dir);
        if (measured === null) {
          measurable = false;
          return;
        }
        if (!isFloor) maxDelta = Math.max(maxDelta, measured - bytes);
        bytes = measured;
      };

      if (primary) record(primary, loadEntry(primary, { memoizeFailure: false }));

      remeasure({ isFloor: true });

      const pending = keys.filter((key) => key !== primary);
      let stoppedBy = "complete";
      let stoppedAt = pending.length;
      let sinceMeasure = 0;

      for (let i = 0; i < pending.length; i++) {
        if (!measurable) {
          stoppedBy = "unmeasured";
          stoppedAt = i;
          break;
        }
        if (Date.now() >= deadline) {
          stoppedBy = "deadline";
          stoppedAt = i;
          break;
        }
        if (bytes + maxDelta > ceiling) {
          stoppedBy = "ceiling";
          stoppedAt = i;
          break;
        }
        record(pending[i], loadEntry(pending[i], { memoizeFailure: false }));
        sinceMeasure += 1;
        if (sinceMeasure >= measureStride(ceiling - bytes, maxDelta)) {
          remeasure();
          sinceMeasure = 0;
        }
      }
      if (sinceMeasure > 0 && measurable) remeasure();

      const skipped = pending.slice(stoppedAt);
      return {
        ok: true,
        state: "warmed",
        entries: keys.length,
        loaded: count,
        failures,
        stoppedBy,
        skipped: skipped.slice(0, MAX_REPORTED_SKIPPED),
        skippedCount: skipped.length,
        bytes,
        dir: cache.dir,
      };
    },
  };
};

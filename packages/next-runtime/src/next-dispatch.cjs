// The dispatcher inside a bundled Lambda artifact: one function serves many
// routes, and the generated launcher hands it the entry table plus its own
// `require` so the table's relative paths resolve against the launcher.
//
// It ships as source rather than as a generated string because it is real logic
// on the request path — see edge-cache-handler.cjs for the same arrangement.
const fs = require("node:fs");
const path = require("node:path");
const { Readable } = require("node:stream");
const { pipeline } = require("node:stream/promises");

// Charged per file on top of its payload because the Go side charges the same
// 512 per tar entry against the same 64 MiB ceiling (tarEntryOverhead in
// bytecode.go). A node-side estimate that undercounts would warm right up to an
// archive the uploader then silently refuses, publishing nothing at all.
const TAR_ENTRY_OVERHEAD = 512;

// Mirrors the constant next-adapter.mts exports as `middlewareEntryKey`. It is
// hardcoded here rather than imported because this file ships as plain CJS
// straight into the bundle — the two are pinned equal by next-adapter.test.mts,
// not by a shared import.
const MIDDLEWARE_ENTRY_KEY = "/_middleware";

// x-forwarded-host/-proto are the client-addressed origin, and are preferred
// over headers.host/req's own scheme for the same reason the membrane reads
// them at all: a request that reached this instance through the edge or a
// loopback self-fetch carries `host` for whichever hop is nearest, not for
// what the client actually addressed. Each can arrive comma-joined — a proxy
// hop appends rather than replaces — so only the first, client-nearest value
// is used.
function middlewareRequestUrl(req) {
  const host = req.headers["x-forwarded-host"] || req.headers.host || "localhost";
  const proto = req.headers["x-forwarded-proto"] || "https";
  return `${String(proto).split(",")[0].trim()}://${String(host).split(",")[0].trim()}${req.url || "/"}`;
}

// Writes an adapter Response onto the node http.ServerResponse the membrane
// gave the dispatcher. Set-Cookie is the one header fetch's Headers folds
// specially (getSetCookie() is the only way to recover every value), so it is
// forwarded through res.setHeader's array form rather than the plain iterator.
async function writeMiddlewareResponse(response, res) {
  res.statusCode = response.status;
  for (const [name, value] of response.headers) {
    if (name.toLowerCase() === "set-cookie") continue;
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

// Writes a generic body: the worker relays this verbatim to the public
// requester (middlewareResponse in workers/nextjs/src/index.ts forwards a
// node-middleware entry's response body untouched, at any status, because it
// cannot tell an adapter-internal failure apart from a response the app
// produced on purpose). `detail`, when given, is the operator-facing half —
// logged here so it still reaches CloudWatch (and scripts/e2e-next/logs.mjs)
// without ever entering the response the client sees.
function fail(res, message, detail) {
  if (detail !== undefined) console.error(`ocel: ${message}: ${detail}`);
  res.statusCode = 502;
  res.end(`ocel: ${message}`);
}

// The contract this mirrors, from Next's own next-server.js: the compiled
// module's default export takes { handler, request, page } and returns
// { response, waitUntil }.
//
// A `proxy.ts` with a top-level await compiles to a module whose exports are
// a Promise rather than the object itself (next-server.js: "if used with top
// level await, this will be a promise") — loadEntry's `require` returns that
// Promise as-is, so it has to be awaited here before `.default` means
// anything. The unwrap happens once per request rather than once per load:
// the same (settled) promise can be awaited any number of times, and a
// module that fails this way fails deterministically, so every request sees
// the same outcome — matching the memoized-failure semantics a synchronous
// require throw already gets.
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
  // 'aborted' is deprecated on IncomingMessage and does not fire behind a
  // Lambda Function URL, so the signal is driven off the response instead:
  // 'close' fires whenever the underlying connection ends, and checking
  // writableEnded is what tells a normal completion (res already wrote
  // everything) apart from the client actually hanging up mid-response.
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

// `getCompileCacheDir`/`flushCompileCache` are absent on older Node, which
// degrades warming to an unsupported reply rather than a version check.
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

// Null rather than a partial total on anything but an absent directory. The
// ceiling is enforced against this number, and an undercount is the dangerous
// direction: it lets the walk sail past the ceiling into an archive the Go
// uploader then refuses outright, publishing nothing where stopping short would
// have published something. A directory node has not created yet is the one
// honest zero, and a file that vanished between the readdir and its stat
// genuinely contributes nothing.
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

// How many entry names a stopped walk names, at most. The list travels in the
// warm reply, through the membrane's summary and into a CloudWatch line and the
// deploy's output, so a bundle with hundreds of cold routes must not be able to
// bloat any of them; the count that travels beside it is the whole truth.
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

// How many entries may be loaded between two measurements. A measurement is a
// flush plus a full recursive walk of the cache directory, so one per entry is
// O(entries x files) inside the very deadline the walk exists to respect — and
// it buys nothing while the total is nowhere near the ceiling, which is where
// most of a bundle's walk happens. The stride is sized so the largest growth
// yet seen, repeated across it, still leaves half the headroom spare; with no
// growth observed yet there is nothing to predict with, so the walk measures
// every entry until there is.
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

  // Case-insensitive, as the worker's own resolver matches these same patterns.
  const dynamic = routes.dynamic.map(([source, key]) => [new RegExp(source, "i"), key]);

  // Which entry serves a pathname. Exact first: a static route always beats a
  // dynamic one that would also match it, which is the worker's precedence.
  // Both tables are keyed by the build's own pathnames, which already carry
  // basePath — Next prefixes it onto every output before the adapter sees one —
  // and so does every URL the app self-fetches. Nothing to add or strip here.
  const entryForPathname = (pathname) => {
    if (Object.prototype.hasOwnProperty.call(routes.exact, pathname)) {
      return routes.exact[pathname];
    }
    for (const [re, key] of dynamic) if (re.test(pathname)) return key;
    return undefined;
  };

  // Each app-page entry owns one route module, whose response cache is built
  // once from whichever request happens to reach it first and then reused for
  // the life of the instance. A PPR resume is a likely first request and runs in
  // minimal mode, which would leave that route's cache minimal — reading and
  // writing nothing durable — for every later request. Building it here, off a
  // request that declares no meta, fixes it non-minimal before any request can.
  //
  // A route module that has no getter is reported rather than skipped: it means
  // Next renamed or dropped it, and the failure it leaves behind is silent — the
  // pin stops happening and ISR quietly ends for any instance a resume reaches
  // first.
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

  // Loading one entry pulls in the chunk graph its routes share, through Next's
  // own chunk ordering — a Turbopack chunk evaluated out of order fails with its
  // dependency's factory unavailable, so an entry is only ever required whole.
  //
  // On the request path failures are memoized alongside successes: a module that
  // throws on import does so deterministically, so re-requiring it per request
  // would only burn CPU on a warm container to reach the same 502. The warm walk
  // passes memoizeFailure: false for the opposite reason — the warmed instance is
  // promoted into production seconds later, and a memo written by a deploy-time
  // walk would answer that route with the same 502 for the life of the instance.
  const loadEntry = (key, { memoizeFailure = true } = {}) => {
    const already = loaded.get(key);
    if (already) return already;
    try {
      const moduleOrPromise = load(entries[key]);
      // A top-level-await module (proxy.ts) requires to a Promise rather than
      // its exports object — runMiddleware awaits it per request, but nothing
      // awaits it here at load time, and an unobserved rejection crashes the
      // process before any request gets the chance. This is that observer: it
      // does not consume the rejection, so runMiddleware's own await still
      // sees and reports it.
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

  // The primary is loaded while the launcher is still being required, which is
  // the Lambda INIT phase: it runs at full vCPU regardless of the function's
  // memory setting, so priming the shared graph there costs nothing. It is
  // best-effort: one broken entry must not take down every route in the bundle,
  // so the failure is reported here and re-surfaced as that key's own 502.
  if (primary) loadEntry(primary);
  // Middleware runs ahead of every route in the bundle, so a request that hits
  // it is the coldest possible request this instance can serve — priming it
  // here for the same reason the primary is primed here. primaryEntryKey never
  // elects it (it is packed into `entries` directly, never into a bundle's
  // route members), so this can't double-load what the line above already did.
  if (Object.prototype.hasOwnProperty.call(entries, MIDDLEWARE_ENTRY_KEY)) {
    loadEntry(MIDDLEWARE_ENTRY_KEY);
  }

  return {
    handler(req, res, ctx) {
      // The worker names the entry on every request it forwards, because only
      // it holds the routing table that resolved the pathname. The app's own
      // loopback self-fetches carry no name — the membrane strips the one they
      // inherited from the request that caused them, which named the
      // originating route — so those resolve here, off the pathname, against
      // the routes this bundle carries.
      const pathname = (req.url || "/").split("?")[0];
      const key =
        typeof req.headers["x-ocel-entry"] === "string"
          ? req.headers["x-ocel-entry"]
          : entryForPathname(pathname);
      if (typeof key !== "string") {
        return fail(res, `no entry serves ${pathname} in this bundle`);
      }
      // No fallback to a default entry: the Function URL is IAM-gated to the
      // edge reader, so every legitimate caller names its entry and a key this
      // bundle lacks is a worker/adapter mismatch worth surfacing.
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
      // Middleware's compiled module exports no `.handler` — it is the
      // adapter-function contract Next's own runtime invokes it with, not a
      // route handler — so it is dispatched through its own path rather than
      // the `handler(req, res, ctx)` call below.
      if (key === MIDDLEWARE_ENTRY_KEY) {
        return runMiddleware(entry.module, req, res, ctx, nextConfig);
      }
      const handler = entry.module?.handler;
      if (typeof handler !== "function") {
        return fail(res, `entry ${key} exports no handler function`);
      }
      return handler(req, res, ctx);
    },

    // Loads every entry's module graph at deploy time so the compile cache the
    // membrane publishes covers the whole bundle instead of one request's slice
    // of it. Modules only: replaying a request would run the app's real
    // handlers, with their real side effects, against a deploy.
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

      // Node buffers compile cache writes in memory, so an unflushed directory
      // measures stale. A cache that cannot be measured leaves the walk with
      // nothing to enforce the ceiling against, which is a stop rather than a
      // zero — see measureCompileCache.
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

      // The primary's graph is already cached from INIT, so this measurement is
      // the floor the later growths are taken against rather than one of them.
      remeasure({ isFloor: true });

      const pending = keys.filter((key) => key !== primary);
      let stoppedBy = "complete";
      let stoppedAt = pending.length;
      let sinceMeasure = 0;

      // A module cannot be unloaded, so crossing the ceiling is unrecoverable —
      // the Go side would refuse to upload the archive and the deploy would
      // publish nothing, strictly worse than the partial cache an unwarmed one
      // leaves behind. The largest growth seen so far stands in for the next
      // stride's, which makes the walk stop while the archive still fits.
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

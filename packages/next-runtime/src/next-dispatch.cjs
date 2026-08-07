// The dispatcher inside a bundled Lambda artifact: one function serves many
// routes, and the generated launcher hands it the entry table plus its own
// `require` so the table's relative paths resolve against the launcher.
//
// It ships as source rather than as a generated string because it is real logic
// on the request path — see edge-cache-handler.cjs for the same arrangement.
const fs = require("node:fs");
const path = require("node:path");

// Charged per file on top of its payload because the Go side charges the same
// 512 per tar entry against the same 64 MiB ceiling (tarEntryOverhead in
// bytecode.go). A node-side estimate that undercounts would warm right up to an
// archive the uploader then silently refuses, publishing nothing at all.
const TAR_ENTRY_OVERHEAD = 512;

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

function measureCompileCache(dir) {
  let total = 0;
  try {
    for (const item of fs.readdirSync(dir, { withFileTypes: true, recursive: true })) {
      if (!item.isFile()) continue;
      total += fs.lstatSync(path.join(item.parentPath, item.name)).size;
      total += TAR_ENTRY_OVERHEAD;
    }
    return total;
  } catch {
    return total;
  }
}

const unsupportedWarmReport = (entryCount) => ({
  ok: false,
  state: "unsupported",
  entries: entryCount,
  loaded: 0,
  failures: [],
  stoppedBy: "complete",
  bytes: 0,
  dir: null,
});

const orUnbounded = (value) => (Number.isFinite(value) ? value : Infinity);

module.exports = function createDispatch({
  entries,
  primary,
  routes = { exact: {}, dynamic: [] },
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

  // Loading one entry pulls in the chunk graph its routes share, through Next's
  // own chunk ordering — a Turbopack chunk evaluated out of order fails with its
  // dependency's factory unavailable, so an entry is only ever required whole.
  //
  // Failures are memoized alongside successes: a module that throws on import
  // does so deterministically, so re-requiring it per request would only burn
  // CPU on a warm container to reach the same 502.
  const loadEntry = (key) => {
    if (!loaded.has(key)) {
      try {
        loaded.set(key, { module: load(entries[key]) });
      } catch (error) {
        console.error(`ocel: entry ${key} failed to load: ${error?.stack ?? error}`);
        loaded.set(key, { error });
      }
    }
    return loaded.get(key);
  };

  // The primary is loaded while the launcher is still being required, which is
  // the Lambda INIT phase: it runs at full vCPU regardless of the function's
  // memory setting, so priming the shared graph there costs nothing. It is
  // best-effort: one broken entry must not take down every route in the bundle,
  // so the failure is reported here and re-surfaced as that key's own 502.
  if (primary) loadEntry(primary);

  // Warming is a deploy-time walk of the whole bundle, so unlike the request
  // path it must not memoize what fails: the warmed instance is promoted into
  // production seconds later, and a memo written here would answer that route
  // with the same 502 for the life of the instance. Successes are memoized —
  // the instance genuinely is warm for them.
  const warmEntry = (key) => {
    const already = loaded.get(key);
    if (already) return already;
    try {
      const entry = { module: load(entries[key]) };
      loaded.set(key, entry);
      return entry;
    } catch (error) {
      return { error };
    }
  };

  const fail = (res, message) => {
    res.statusCode = 502;
    res.end(`ocel: ${message}`);
  };

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

      // Node buffers compile cache writes in memory, so an unflushed directory
      // measures stale.
      const measure = () => {
        cache.flush();
        return measureCompileCache(cache.dir);
      };

      if (primary) record(primary, warmEntry(primary));

      // The primary's graph is already cached from INIT, so this measurement is
      // the floor the per-entry growths are taken against.
      let bytes = measure();
      let maxDelta = 0;
      let stoppedBy = "complete";

      // A module cannot be unloaded, so crossing the ceiling is unrecoverable —
      // the Go side would refuse to upload the archive and the deploy would
      // publish nothing, strictly worse than the partial cache an unwarmed one
      // leaves behind. The largest growth seen so far stands in for the next
      // entry's, which makes the walk stop while the archive still fits.
      for (const key of keys) {
        if (key === primary) continue;
        if (Date.now() >= deadline) {
          stoppedBy = "deadline";
          break;
        }
        if (bytes + maxDelta > ceiling) {
          stoppedBy = "ceiling";
          break;
        }
        record(key, warmEntry(key));
        const measured = measure();
        maxDelta = Math.max(maxDelta, measured - bytes);
        bytes = measured;
      }

      return {
        ok: true,
        state: "warmed",
        entries: keys.length,
        loaded: count,
        failures,
        stoppedBy,
        bytes,
        dir: cache.dir,
      };
    },
  };
};

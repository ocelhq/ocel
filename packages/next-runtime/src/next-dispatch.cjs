// The dispatcher inside a bundled Lambda artifact: one function serves many
// routes, and the generated launcher hands it the entry table plus its own
// `require` so the table's relative paths resolve against the launcher.
//
// It ships as source rather than as a generated string because it is real logic
// on the request path — see edge-cache-handler.cjs for the same arrangement.

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
  };
};

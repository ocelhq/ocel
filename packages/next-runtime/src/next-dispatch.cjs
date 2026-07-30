// The dispatcher inside a bundled Lambda artifact: one function serves many
// routes, and the generated launcher hands it the entry table plus its own
// `require` so the table's relative paths resolve against the launcher.
//
// It ships as source rather than as a generated string because it is real logic
// on the request path — see edge-cache-handler.cjs for the same arrangement.

module.exports = function createDispatch({ entries, primary, load }) {
  const loaded = new Map();

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
      const key = req.headers["x-ocel-entry"];
      if (typeof key !== "string") {
        return fail(res, "request carries no x-ocel-entry header");
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

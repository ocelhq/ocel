// The cache handler `next build` compiles into every edge chunk, named through
// config.cacheHandler. One file with a module-scope branch, because that single
// config value is instantiated on two tiers: Turbopack statically replaces
// process.env.NEXT_RUNTIME and drops the branch it did not take, so the edge
// chunk carries the RPC client alone while `next build`'s static-generation
// workers — which fall back to an in-process FileSystemCache when the handler is
// falsy — still get a real incremental cache.

if (process.env.NEXT_RUNTIME === "edge") {
  // The binding cannot arrive through process.env: at the edge those reads are
  // build-time string literals, and a service binding is not a string. The
  // dynamic worker's shim assigns this global from the worker's load-time env
  // before the first Next chunk evaluates.
  const resolveCacheBinding = () => {
    const cache = globalThis.__OCEL_EDGE_CACHE;
    if (!cache || !cache.rpc) {
      throw new Error(
        "ocel: no cache binding on globalThis.__OCEL_EDGE_CACHE — the edge bundle was loaded without one",
      );
    }
    return cache;
  };

  const tagsOf = (ctx) => [...(ctx?.tags ?? []), ...(ctx?.softTags ?? [])];

  module.exports = class OcelEdgeCacheHandler {
    // Next does not wrap get() in a try/catch: a throw surfaces as a render
    // error rather than a miss. Every failure is therefore swallowed into null,
    // which degrades a cache outage into a fresh fetch instead of a 500.
    async get(key, ctx) {
      if (ctx?.kind !== "FETCH") return null;
      try {
        const { rpc, scope } = resolveCacheBinding();
        return await rpc.fetchGet(scope, key, tagsOf(ctx));
      } catch {
        return null;
      }
    }

    // Next's node entry templates are bundled into every edge chunk as dead
    // code, so a future Next could route a page-kind write through here with no
    // change on our side. Failing loudly keeps FETCH-only an enforced invariant
    // rather than an assumption — IncrementalCache.set catches and warns, so the
    // response survives it.
    //
    // The promise is returned, not detached: Next hands a background
    // revalidation's set() to evt.waitUntil, which settles it after the
    // response, whereas a promise nobody holds is cancelled with the request.
    async set(key, data, ctx) {
      if (!data) return;
      if (data.kind !== "FETCH") {
        throw new Error(
          `ocel: the edge cache handler stores fetch entries only, got kind ${data.kind}`,
        );
      }
      const { rpc, scope } = resolveCacheBinding();
      const tags = ctx?.tags ?? [];
      const entry = {
        lastModified: Date.now(),
        value: {
          kind: "FETCH",
          data: data.data,
          revalidate: data.revalidate,
          tags,
        },
      };
      return rpc.fetchSet(scope, key, entry, tags);
    }

    // Recorded centrally rather than in memory: an invalidation raised at one
    // edge location has to reach every instance and the Lambda tier alike.
    async revalidateTag(tags, durations) {
      const list = typeof tags === "string" ? [tags] : tags;
      if (list.length === 0) return;
      const { rpc, scope } = resolveCacheBinding();
      return rpc.revalidateTags(scope, list, durations);
    }

    // No per-request memo is held, so there is nothing to reset.
    resetRequestCache() {}
  };
} else {
  module.exports =
    require("next/dist/server/lib/incremental-cache/file-system-cache").default;
}

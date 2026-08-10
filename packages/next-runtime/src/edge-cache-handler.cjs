if (process.env.NEXT_RUNTIME === "edge") {
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
    async get(key, ctx) {
      if (ctx?.kind !== "FETCH") return null;
      try {
        const { rpc, scope } = resolveCacheBinding();
        return await rpc.fetchGet(scope, key, tagsOf(ctx));
      } catch {
        return null;
      }
    }

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

    async revalidateTag(tags, durations) {
      const list = typeof tags === "string" ? [tags] : tags;
      if (list.length === 0) return;
      const { rpc, scope } = resolveCacheBinding();
      return rpc.revalidateTags(scope, list, durations);
    }

    resetRequestCache() {}
  };
} else {
  module.exports =
    require("next/dist/server/lib/incremental-cache/file-system-cache").default;
}

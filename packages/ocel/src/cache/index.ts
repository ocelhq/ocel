const cacheHost = Symbol.for("ocel.cache.host");

type RevalidationMode = "purge" | "invalidate";

interface CacheHost {
  tag(tags: readonly string[]): void;
  revalidateTag(tag: string, mode: RevalidationMode): void;
  revalidatePath(path: string, mode: RevalidationMode): void;
}

function findCacheHost(): CacheHost | undefined {
  return (globalThis as Record<symbol, CacheHost | undefined>)[cacheHost];
}

/**
 * How a revalidation treats what it names. Leaving options out marks it stale, so the next
 * request is answered with the stale copy while a fresh one is made. `{ expire: 0 }` purges it,
 * so the next request waits for a fresh render.
 */
export interface RevalidateOptions {
  expire?: 0;
}

function readRevalidationMode(
  call: string,
  options: RevalidateOptions | undefined,
): RevalidationMode {
  if (options === undefined) return "invalidate";
  if (typeof options !== "object" || options === null || Array.isArray(options)) {
    throw new TypeError(`ocel: ${call} takes options such as { expire: 0 }`);
  }
  if (options.expire === undefined) return "invalidate";
  if (options.expire !== 0) {
    throw new TypeError(
      `ocel: ${call} knows the option { expire: 0 }, not expire ${String(options.expire)}`,
    );
  }
  return "purge";
}

/**
 * Tags the response the running request is answering, so a later `revalidateTag` can name it.
 * Call it while handling the request, before the response is sent. It does nothing where no
 * edge caches the app's responses, such as `ocel dev`.
 */
export function cacheTag(...tags: string[]): void {
  for (const tag of tags) {
    if (typeof tag !== "string" || tag === "") {
      throw new TypeError("ocel: a cache tag is a non-empty string");
    }
  }
  findCacheHost()?.tag(tags);
}

/**
 * Revalidates every cached response tagged with `tag` through `cacheTag`. They are marked stale
 * and served while a fresh copy is made; with `{ expire: 0 }` they are purged, and the next
 * request renders fresh. Call it while handling a request. It does nothing where no edge
 * caches the app's responses.
 */
export function revalidateTag(tag: string, options?: RevalidateOptions): void {
  if (typeof tag !== "string" || tag === "") {
    throw new TypeError("ocel: a cache tag is a non-empty string");
  }
  findCacheHost()?.revalidateTag(tag, readRevalidationMode("revalidateTag", options));
}

/**
 * Revalidates the cached response for the page at `path`, as `revalidateTag` does. `path` is a
 * pathname such as `/posts/1`, with no query. It does nothing where no edge caches the app's
 * responses.
 */
export function revalidatePath(path: string, options?: RevalidateOptions): void {
  if (typeof path !== "string" || !path.startsWith("/") || /[?#]/.test(path)) {
    throw new TypeError("ocel: a revalidated path starts with a slash and has no query");
  }
  findCacheHost()?.revalidatePath(path, readRevalidationMode("revalidatePath", options));
}

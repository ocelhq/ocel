const cacheHost = Symbol.for("ocel.cache.host");

interface CacheHost {
  tag(tags: readonly string[]): void;
  revalidateTag(tag: string, mode: "expire" | "stale"): void;
  revalidatePath(path: string): void;
}

function host(): CacheHost | undefined {
  return (globalThis as Record<symbol, CacheHost | undefined>)[cacheHost];
}

/**
 * How `revalidateTag` treats what a tag names. `"max"` serves it stale while a fresh copy is
 * made in the background; leaving it out expires it at once.
 */
export type RevalidateProfile = "max";

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
  host()?.tag(tags);
}

/**
 * Revalidates every cached response tagged with `tag` through `cacheTag`. With no profile they
 * expire at once and the next request renders fresh; with `"max"` they are served stale while a
 * fresh copy is made. Call it while handling a request, and expect it to reach only an edge
 * that purges for the app. It does nothing where no edge caches the app's responses.
 */
export function revalidateTag(tag: string, profile?: RevalidateProfile): void {
  if (typeof tag !== "string" || tag === "") {
    throw new TypeError("ocel: a cache tag is a non-empty string");
  }
  if (profile !== undefined && profile !== "max") {
    throw new TypeError(`ocel: revalidateTag knows the profile "max", not ${String(profile)}`);
  }
  host()?.revalidateTag(tag, profile === "max" ? "stale" : "expire");
}

/**
 * Revalidates the cached response for the page at `path`, as `revalidateTag` does with no
 * profile. `path` is a pathname such as `/posts/1`, with no query. It does nothing where no
 * edge caches the app's responses.
 */
export function revalidatePath(path: string): void {
  if (typeof path !== "string" || !path.startsWith("/") || /[?#]/.test(path)) {
    throw new TypeError("ocel: a revalidated path starts with a slash and has no query");
  }
  host()?.revalidatePath(path);
}

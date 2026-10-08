import { afterEach, expect, test } from "vitest";
import OcelCacheHandler from "../src/cache-handler.mjs";
import { installCacheHandlers } from "../src/cache-handlers.mjs";
import useCacheDefault from "../src/use-cache-default.mjs";
import useCacheRemote from "../src/use-cache-remote.mjs";

const nextCacheHandlers = Symbol.for("@next/cache-handlers");

afterEach(() => {
  delete (globalThis as Record<symbol, unknown>)[nextCacheHandlers];
});

test("installs the fetch, default and remote handlers as Next's global cache handlers", () => {
  installCacheHandlers();

  expect((globalThis as Record<symbol, unknown>)[nextCacheHandlers]).toEqual({
    FetchCache: OcelCacheHandler,
    DefaultCache: useCacheDefault,
    RemoteCache: useCacheRemote,
  });
});

test("hands back the fetch cache handler it installed", () => {
  expect(installCacheHandlers()).toBe(OcelCacheHandler);
});

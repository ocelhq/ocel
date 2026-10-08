import { afterEach, expect, test } from "vitest";
import OcelCacheHandler from "../src/cache-handler.mjs";
import { getNextHost, installNextHost } from "../src/host.mjs";
import { installServerPreload } from "../src/server-preload.mjs";
import useCacheDefault from "../src/use-cache-default.mjs";
import useCacheRemote from "../src/use-cache-remote.mjs";

const nextCacheHandlers = Symbol.for("@next/cache-handlers");
const nextRouterServerContexts = Symbol.for("@next/router-server-methods");
const slots = globalThis as Record<symbol, unknown>;

afterEach(() => {
  delete slots[nextCacheHandlers];
  delete slots[nextRouterServerContexts];
  installNextHost({});
});

function registerNextServer(context: Record<string, unknown>): Record<string, unknown> {
  if (!slots[nextRouterServerContexts]) slots[nextRouterServerContexts] = {};
  const contexts = slots[nextRouterServerContexts] as Record<string, Record<string, unknown>>;
  contexts[""] = context;
  return contexts[""];
}

const nextDefaults = {
  cacheHandler: undefined,
  cacheHandlers: { default: undefined, remote: undefined, static: undefined },
};

test("installs the fetch, default and remote handlers as Next's global cache handlers", () => {
  installServerPreload(() => ({}));

  expect(slots[nextCacheHandlers]).toEqual({
    FetchCache: OcelCacheHandler,
    DefaultCache: useCacheDefault,
    RemoteCache: useCacheRemote,
  });
});

test("builds its host only when the cache first reads it", () => {
  let built = 0;

  installServerPreload(() => {
    built++;
    return { cacheTagsPerObject: 3 };
  });

  expect(built).toBe(0);
  expect(getNextHost().cacheTagsPerObject).toBe(3);
  expect(built).toBe(1);
});

test.each([
  ["cacheHandler", { cacheHandler: "/app/h.cjs" }],
  ["cacheHandlers.default", { cacheHandlers: { default: "/app/d.cjs" } }],
  ["cacheHandlers.remote", { cacheHandlers: { remote: "/app/r.cjs" } }],
])(
  "refuses the config a Next server loads when its %s names the app's own handler",
  (setting, config) => {
    installServerPreload(() => ({}));

    expect(() => registerNextServer({ nextConfig: { ...nextDefaults, ...config } })).toThrow(
      setting,
    );
  },
);

test("refuses a config Next sets on a server it registered without one", () => {
  installServerPreload(() => ({}));
  const context = registerNextServer({});

  expect(() => {
    context.nextConfig = { cacheHandler: "/app/h.cjs" };
  }).toThrow("cacheHandler");
});

test("builds its host as a Next server loads a config that names no handler of its own", () => {
  let built = 0;
  installServerPreload(() => {
    built++;
    return {};
  });

  const context = registerNextServer({ nextConfig: nextDefaults, hostname: "localhost" });

  expect(built).toBe(1);
  expect(context.hostname).toBe("localhost");
  expect(context.nextConfig).toBe(nextDefaults);
});

test("stops a Next server loading its config when its host cannot be built", () => {
  installServerPreload(() => {
    throw new Error("ocel: OCEL_CDN_URL_MAP names a url map to purge");
  });

  expect(() => registerNextServer({ nextConfig: nextDefaults })).toThrow("OCEL_CDN_URL_MAP");
});

test("never builds the host of a process that runs no Next server", () => {
  let built = 0;

  installServerPreload(() => {
    built++;
    return {};
  });

  expect(built).toBe(0);
});

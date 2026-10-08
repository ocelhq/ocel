import { afterEach, expect, test } from "vitest";
import OcelCacheHandler from "../src/cache-handler.mjs";
import { newServerAdapter } from "../src/server-adapter.mjs";
import useCacheDefault from "../src/use-cache-default.mjs";
import useCacheRemote from "../src/use-cache-remote.mjs";

const server = { phase: "phase-production-server" };
const nextCacheHandlers = Symbol.for("@next/cache-handlers");

afterEach(() => {
  delete (globalThis as Record<symbol, unknown>)[nextCacheHandlers];
});

test("next start serves through the global handlers its adapter installs", () => {
  const adapter = newServerAdapter(() => {});

  const config = adapter.modifyConfig({ output: "standalone" }, server);

  expect(config).toEqual({ output: "standalone", cacheMaxMemorySize: 0 });
  expect((globalThis as Record<symbol, unknown>)[nextCacheHandlers]).toEqual({
    FetchCache: OcelCacheHandler,
    DefaultCache: useCacheDefault,
    RemoteCache: useCacheRemote,
  });
});

test.each([
  [{ cacheHandler: "/app/cache-handler.cjs" }, "cacheHandler", "NEXT_CACHE_HANDLER_PATH"],
  [
    { cacheHandlers: { default: "/app/use-cache.cjs" } },
    "cacheHandlers.default",
    "NEXT_DEFAULT_CACHE_HANDLER_PATH",
  ],
  [
    { cacheHandlers: { remote: "/app/use-cache.cjs" } },
    "cacheHandlers.remote",
    "NEXT_REMOTE_CACHE_HANDLER_PATH",
  ],
])(
  "refuses to serve an app whose config names its own cache handler in %j",
  (config, setting, envVar) => {
    const adapter = newServerAdapter(() => {});

    const run = () => adapter.modifyConfig(config, server);

    expect(run).toThrow(setting);
    expect(run).toThrow(envVar);
  },
);

test("serves an app whose cache handlers name only kinds Ocel does not install", () => {
  const adapter = newServerAdapter(() => {});
  const cacheHandlers = { default: undefined, remote: undefined, static: "/app/static.cjs" };

  const config = adapter.modifyConfig({ cacheHandler: undefined, cacheHandlers }, server);

  expect(config.cacheHandlers).toEqual(cacheHandlers);
});

test("leaves a build's config exactly as the build configured it", () => {
  let installs = 0;
  const adapter = newServerAdapter(() => installs++);
  const config = { cacheHandler: "mine.js" };

  expect(adapter.modifyConfig(config, { phase: "phase-production-build" })).toBe(config);
  expect(installs).toBe(0);
  expect((globalThis as Record<symbol, unknown>)[nextCacheHandlers]).toBeUndefined();
});

test("installs its host once however often Next loads the config", () => {
  let installs = 0;
  const adapter = newServerAdapter(() => installs++);

  adapter.modifyConfig({}, server);
  adapter.modifyConfig({}, server);
  adapter.modifyConfig({}, server);

  expect(installs).toBe(1);
});

import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { CacheHandlerSettings } from "@framework/next-cache/app-cache-handlers";
import { afterEach, beforeEach, expect, test } from "vitest";
import OcelCacheHandler from "../src/cache-handler.mjs";
import { getNextHost, installNextHost } from "../src/host.mjs";
import { installServerPreload, readServedCacheHandlers } from "../src/server-preload.mjs";
import useCacheDefault from "../src/use-cache-default.mjs";
import useCacheRemote from "../src/use-cache-remote.mjs";

const nextCacheHandlers = Symbol.for("@next/cache-handlers");
const listen = net.Server.prototype.listen;
let projectDir: string;

beforeEach(async () => {
  projectDir = await mkdtemp(join(tmpdir(), "server-preload-"));
});

afterEach(async () => {
  net.Server.prototype.listen = listen;
  delete (globalThis as Record<symbol, unknown>)[nextCacheHandlers];
  installNextHost({});
  await rm(projectDir, { recursive: true, force: true });
});

async function writeManifest(config: Record<string, unknown>): Promise<void> {
  await mkdir(join(projectDir, ".next"), { recursive: true });
  await writeFile(join(projectDir, ".next/required-server-files.json"), JSON.stringify({ config }));
}

test("installs the fetch, default and remote handlers as Next's global cache handlers", () => {
  installServerPreload(() => ({}));

  expect((globalThis as Record<symbol, unknown>)[nextCacheHandlers]).toEqual({
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

test("reads the cache handlers a built app serves with from its build manifest", async () => {
  await writeManifest({ cacheHandler: "../h.cjs", cacheHandlers: { remote: "../r.cjs" } });

  expect(readServedCacheHandlers({}, projectDir)).toEqual({
    cacheHandler: "../h.cjs",
    cacheHandlers: { default: undefined, remote: "../r.cjs" },
  });
});

test.each([
  ["NEXT_CACHE_HANDLER_PATH", (served: CacheHandlerSettings) => served.cacheHandler],
  [
    "NEXT_DEFAULT_CACHE_HANDLER_PATH",
    (served: CacheHandlerSettings) => served.cacheHandlers?.default,
  ],
  [
    "NEXT_REMOTE_CACHE_HANDLER_PATH",
    (served: CacheHandlerSettings) => served.cacheHandlers?.remote,
  ],
])("reads the cache handler %s names", async (name, handlerOf) => {
  await writeManifest({});

  const served = readServedCacheHandlers({ [name]: "/e.cjs" }, projectDir);

  expect(handlerOf(served)).toBe("/e.cjs");
});

test("reads a standalone server's baked config and ignores the environment its config replaced", () => {
  const env = {
    __NEXT_PRIVATE_STANDALONE_CONFIG: JSON.stringify({ cacheHandlers: { default: "../u.cjs" } }),
    NEXT_CACHE_HANDLER_PATH: "/ignored.cjs",
  };

  expect(readServedCacheHandlers(env, projectDir)).toEqual({
    cacheHandlers: { default: "../u.cjs" },
  });
});

test("refuses to read an app whose build manifest is missing", () => {
  expect(() => readServedCacheHandlers({}, projectDir)).toThrow("required-server-files.json");
});

test("refuses the first server to listen when the app names its own cache handler", async () => {
  await writeManifest({ cacheHandler: "../h.cjs" });
  const cwd = process.cwd();
  process.chdir(projectDir);
  try {
    installServerPreload(() => ({}));

    const run = () => net.createServer().listen(0);

    expect(run).toThrow("cacheHandler");
  } finally {
    process.chdir(cwd);
  }
});

test("lets a server listen when the app names no cache handler of its own", async () => {
  await writeManifest({ cacheHandlers: {} });
  const cwd = process.cwd();
  process.chdir(projectDir);
  try {
    installServerPreload(() => ({}));

    const server = net.createServer().listen(0);

    await new Promise((resolve) => server.close(resolve));
  } finally {
    process.chdir(cwd);
  }
});

test("leaves a process that never listens alone however its app is configured", async () => {
  await writeManifest({ cacheHandler: "../h.cjs" });

  installServerPreload(() => ({}));

  expect(getNextHost()).toBeDefined();
});

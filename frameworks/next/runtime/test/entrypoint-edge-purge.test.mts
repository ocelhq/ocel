import { mkdtemp, readdir, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { TagRecord } from "@platform/edge-contract/purge";
import { afterAll, beforeAll, expect, test } from "vitest";
import { installNextHost } from "../src/host.mjs";
import {
  withLauncherConfig,
  writeNextProjectFixture,
} from "../test-support/next-project-fixture.mjs";

const launcherModule = `module.exports = { async handler(req, res) { res.end("up"); } };
`;

const nextCacheHandlers = Symbol.for("@next/cache-handlers");

type FetchCache = new (
  ctx: object,
) => { revalidateTag(tags: string | string[], durations?: { expire?: number }): Promise<void> };

let dir: string;
let projectDir: string;
const purged: ReadonlyMap<string, TagRecord>[] = [];

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-next-edge-purge-"));
  projectDir = join(dir, "project");
  const fixture = await writeNextProjectFixture(projectDir);
  const launcherPath = join(projectDir, "__next_launcher.cjs");
  await writeFile(launcherPath, withLauncherConfig(launcherModule, fixture.nextConfig));

  const port = await freePort();
  process.env.PORT = String(port);
  process.env.OCEL_HANDLER = launcherPath;
  installNextHost({
    bind: { host: "127.0.0.1", port },
    edgePurge: async (tags) => {
      purged.push(tags);
    },
  });
  await import("../src/entrypoint.mjs");
  await answer(port);
});

afterAll(async () => {
  delete process.env.PORT;
  delete process.env.OCEL_HANDLER;
  delete (globalThis as Record<symbol, unknown>)[nextCacheHandlers];
  await rm(dir, { recursive: true, force: true });
});

function installedFetchCache(): FetchCache | undefined {
  return (globalThis as Record<symbol, { FetchCache?: FetchCache } | undefined>)[nextCacheHandlers]
    ?.FetchCache;
}

test("a host that keeps no cache store and names an edge purge keeps Next's own FileSystemCache", () => {
  const { default: FileSystemCache } = createRequire(join(projectDir, "package.json"))(
    "next/dist/server/lib/incremental-cache/file-system-cache.js",
  );

  expect(installedFetchCache()?.prototype).toBeInstanceOf(FileSystemCache);
});

test("a tag Next revalidates is purged at the edge, expired now when no durations stale it", async () => {
  purged.length = 0;
  const FetchCache = installedFetchCache()!;
  const before = Date.now();

  await new FetchCache({ serverDistDir: join(projectDir, ".next", "server") }).revalidateTag([
    "posts",
    "home",
  ]);

  expect(purged).toHaveLength(1);
  expect([...purged[0]!.keys()]).toEqual(["posts", "home"]);
  expect(purged[0]!.get("posts")!.expired).toBeGreaterThanOrEqual(before);
});

test("a tag Next revalidates with durations is purged stale now and expired once they run out", async () => {
  purged.length = 0;
  const FetchCache = installedFetchCache()!;

  await new FetchCache({ serverDistDir: join(projectDir, ".next", "server") }).revalidateTag(
    "posts",
    { expire: 60 },
  );

  const record = purged[0]!.get("posts")!;
  expect(record.expired! - record.stale!).toBe(60_000);
});

test("Next's cache on a host the edge purges keeps nothing on the container's disk, so a restarted container serves nothing it cached before", async () => {
  const require = createRequire(join(projectDir, "package.json"));
  const { nodeFs } = require("next/dist/server/lib/node-fs-methods.js");
  const serverDistDir = join(projectDir, ".next", "server");
  const FetchCache = installedFetchCache() as unknown as new (
    ctx: object,
  ) => {
    set(key: string, data: object, ctx: object): Promise<void>;
    get(key: string, ctx: object): Promise<unknown>;
  };
  const ctx = { fs: nodeFs, serverDistDir, flushToDisk: true, maxMemoryCacheSize: 0 };
  const data = { headers: {}, body: "fetched", status: 200, url: "https://api.example/" };

  await new FetchCache(ctx).set(
    "fetch-key",
    { kind: "FETCH", data, revalidate: 60 },
    { fetchCache: true, revalidate: 60, tags: [] },
  );

  expect(
    await new FetchCache(ctx).get("fetch-key", { kind: "FETCH", revalidate: 60, tags: [] }),
  ).toBeNull();
  expect(await readdir(join(projectDir, ".next", "cache", "fetch-cache")).catch(() => [])).toEqual(
    [],
  );
});

async function answer(port: number): Promise<string> {
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      return await (await fetch(`http://127.0.0.1:${port}/`)).text();
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
  }
  throw new Error(`nothing answered on ${port}`);
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.on("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const { port } = probe.address() as net.AddressInfo;
      probe.close(() => resolve(port));
    });
  });
}

import { execFile } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { isBuiltin } from "node:module";
import { tmpdir } from "node:os";
import { join, relative, resolve } from "node:path";
import { promisify } from "node:util";
import { init, parse } from "es-module-lexer";
import { afterAll, beforeAll, expect, test } from "vitest";

const execFileAsync = promisify(execFile);
const pkgDir = resolve(import.meta.dirname, "..");

let dist: string;
let preload: string;

beforeAll(async () => {
  dist = await mkdtemp(join(tmpdir(), "ocel-aws-preload-"));
  await execFileAsync("bun", ["scripts/bundle-modules.mjs", relative(pkgDir, dist)], {
    cwd: pkgDir,
  });
  preload = join(dist, "container", "server-preload.mjs");
}, 120_000);

afterAll(async () => {
  await rm(dist, { recursive: true, force: true });
});

const cacheEnv = {
  OCEL_ISR_BUCKET: "bucket",
  OCEL_ISR_PREFIX: "prod/shop/web/r0a1b2c3d/isr",
  OCEL_STATE_TABLE: "state",
  OCEL_ISR_TAG_NAMESPACE: "PROJECT#shop#STACK#prod--web--r0a1b2c3d#TAG#",
};

const registerNextServer = (config: string) =>
  `globalThis[Symbol.for("@next/router-server-methods")] ??= {};
globalThis[Symbol.for("@next/router-server-methods")][""] = { nextConfig: ${config} };`;

test("the server preload imports nothing but Node's own modules", async () => {
  await init;
  const [imports] = parse(await readFile(preload, "utf8"));
  const bare = imports
    .flatMap((found) => (found.n === undefined ? [] : [found.n]))
    .filter((specifier) => !isBuiltin(specifier));
  expect(bare).toEqual([]);
});

test("the server preload installs the cache handlers and builds the AWS host once a Next server loads its config", async () => {
  const { stdout } = await execFileAsync(
    process.execPath,
    [
      "--input-type=module",
      "-e",
      `await import(${JSON.stringify(preload)});
const handlers = globalThis[Symbol.for("@next/cache-handlers")];
const before = { slot: typeof globalThis[Symbol.for("ocel.next.host.v1")] };
${registerNextServer("{ cacheHandlers: {} }")}
const host = globalThis[Symbol.for("ocel.next.host.v1")];
process.stdout.write(JSON.stringify({
  before,
  after: { store: typeof host.newCacheStore, useCache: typeof host.newUseCacheStore, tags: host.cacheTagsPerObject },
  handlers: Object.fromEntries(Object.entries(handlers ?? {}).map(([name, handler]) => [name, typeof handler])),
}));`,
    ],
    { env: { PATH: process.env.PATH, ...cacheEnv } },
  );

  expect(JSON.parse(stdout)).toEqual({
    before: { slot: "function" },
    after: { store: "function", useCache: "function", tags: 50 },
    handlers: { FetchCache: "function", DefaultCache: "object", RemoteCache: "object" },
  });
});

test("the server preload withholds a response's shared-cache lifetime from CloudFront", async () => {
  const { stdout } = await execFileAsync(
    process.execPath,
    [
      "--input-type=module",
      "-e",
      `import http from "node:http";
await import(${JSON.stringify(preload)});
const server = http.createServer((req, res) => {
  res.setHeader("cache-control", "s-maxage=60, stale-while-revalidate");
  res.end("x");
});
await new Promise((done) => server.listen(0, "127.0.0.1", done));
const res = await fetch("http://127.0.0.1:" + server.address().port);
process.stdout.write(res.headers.get("cache-control") ?? "");
server.close();`,
    ],
    { env: { PATH: process.env.PATH } },
  );

  expect(stdout).not.toContain("s-maxage");
});

test("a process running the server preload stops when the Next server it runs loads a config naming the app's own cache handler", async () => {
  const run = execFileAsync(
    process.execPath,
    [
      "--input-type=module",
      "-e",
      `await import(${JSON.stringify(preload)});
${registerNextServer(`{ cacheHandler: "/app/mine.cjs" }`)}`,
    ],
    { env: { PATH: process.env.PATH } },
  );

  await expect(run).rejects.toMatchObject({
    stderr: expect.stringContaining("has Next use the app's own cache handler"),
  });
});

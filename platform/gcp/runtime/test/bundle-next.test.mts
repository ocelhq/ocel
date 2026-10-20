import { type ChildProcess, execFile, spawn } from "node:child_process";
import { mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { isBuiltin } from "node:module";
import net from "node:net";
import { tmpdir } from "node:os";
import { join, relative, resolve } from "node:path";
import { promisify } from "node:util";
import {
  withLauncherConfig,
  writeNextProjectFixture,
} from "@framework/next-runtime/test-support/next-project-fixture";
import { init, parse } from "es-module-lexer";
import { afterAll, afterEach, beforeAll, expect, test } from "vitest";

const execFileAsync = promisify(execFile);
const pkgDir = resolve(import.meta.dirname, "..");

const children: ChildProcess[] = [];
let dist: string;
let dir: string;

beforeAll(async () => {
  dist = await mkdtemp(join(tmpdir(), "ocel-gcp-next-"));
  await execFileAsync("bun", ["scripts/bundle-next.mjs", relative(pkgDir, dist)], {
    cwd: pkgDir,
  });
  dir = join(dist, "next");
}, 120_000);

afterAll(async () => {
  await rm(dist, { recursive: true, force: true });
});

afterEach(() => {
  for (const child of children.splice(0)) child.kill("SIGKILL");
});

async function shippedFiles(): Promise<string[]> {
  const entries = await readdir(dir, { recursive: true, withFileTypes: true });
  return entries
    .filter((entry) => entry.isFile())
    .map((entry) => relative(dir, join(entry.parentPath, entry.name)))
    .sort();
}

function freePort(): Promise<number> {
  return new Promise((done, fail) => {
    const probe = net.createServer();
    probe.on("error", fail);
    probe.listen({ host: "127.0.0.1", port: 0 }, () => {
      const { port } = probe.address() as net.AddressInfo;
      probe.close(() => done(port));
    });
  });
}

async function answer(port: number): Promise<string> {
  for (let attempt = 0; attempt < 200; attempt++) {
    try {
      return await (await fetch(`http://127.0.0.1:${port}/`)).text();
    } catch {
      await new Promise((wait) => setTimeout(wait, 25));
    }
  }
  throw new Error(`nothing answered on port ${port}`);
}

test("the runtime directory holds the entrypoint and the server preload, with the cache handlers inside them, and libvips's notices", async () => {
  const own = (await shippedFiles()).filter((file) => !file.startsWith("node_modules/"));
  expect(own).toEqual([
    "GPL-3.0.txt",
    "LGPL-3.0.txt",
    "THIRD_PARTY_NOTICES",
    "entrypoint.mjs",
    "server-preload.mjs",
  ]);
});

test("the node serve entry is a module of its own that boots a function from OCEL_HANDLER and installs the edge purge", async () => {
  const serve = join(dist, "node", "serve.mjs");
  await init;
  const [imports] = parse(await readFile(serve, "utf8"));
  const bare = imports
    .flatMap((found) => (found.n === undefined ? [] : [found.n]))
    .filter((specifier) => !isBuiltin(specifier));
  expect(bare).toEqual([]);

  const failed = await execFileAsync(process.execPath, [serve], {
    env: { PATH: process.env.PATH, OCEL_HANDLER: "", PORT: "8080" },
  }).catch((err) => err);
  expect(failed.code).toBe(1);
  expect(failed.stderr).toContain("OCEL_HANDLER");
});

test("the node serve entry refuses to boot a service told a url map and no release to clear tags of", async () => {
  const failed = await execFileAsync(process.execPath, [join(dist, "node", "serve.mjs")], {
    env: {
      PATH: process.env.PATH,
      OCEL_HANDLER: "/app/x.mjs",
      PORT: "8080",
      OCEL_CDN_URL_MAP: "m",
    },
  }).catch((err) => err);

  expect(failed.code).not.toBe(0);
  expect(failed.stderr).toContain("OCEL_CDN_URL_MAP");
});

test("the entrypoint imports nothing but Node's own modules and the sharp the directory ships", async () => {
  await init;
  const [imports] = parse(await readFile(join(dir, "entrypoint.mjs"), "utf8"));
  const bare = imports
    .flatMap((found) => (found.n === undefined ? [] : [found.n]))
    .filter((specifier) => !isBuiltin(specifier));
  expect([...new Set(bare)]).toEqual(["sharp"]);
});

test("the server preload imports nothing but Node's own modules", async () => {
  await init;
  const [imports] = parse(await readFile(join(dir, "server-preload.mjs"), "utf8"));
  const bare = imports
    .flatMap((found) => (found.n === undefined ? [] : [found.n]))
    .filter((specifier) => !isBuiltin(specifier));
  expect(bare).toEqual([]);
});

const cacheEnv = {
  PORT: "8080",
  OCEL_EDGE_BUCKET: "edge",
  OCEL_CACHE_BUCKET: "cache",
  OCEL_STORAGE_COORDINATE: "shop/prod/web/r1a2b3c4d",
  OCEL_TAG_DATABASE: "database",
};

const registerNextServer = (config: string) =>
  `globalThis[Symbol.for("@next/router-server-methods")] ??= {};
globalThis[Symbol.for("@next/router-server-methods")][""] = { nextConfig: ${config} };`;

test("the server preload installs the cache handlers and builds its Cloud Run host only once a Next server loads its config", async () => {
  const { stdout } = await execFileAsync(
    process.execPath,
    [
      "--input-type=module",
      "-e",
      `await import(${JSON.stringify(join(dir, "server-preload.mjs"))});
const handlers = globalThis[Symbol.for("@next/cache-handlers")];
const before = { slot: typeof globalThis[Symbol.for("ocel.next.host.v1")] };
${registerNextServer("{ cacheHandlers: {} }")}
const host = globalThis[Symbol.for("ocel.next.host.v1")];
process.stdout.write(JSON.stringify({
  before,
  after: { store: typeof host.newCacheStore },
  handlers: Object.fromEntries(Object.entries(handlers ?? {}).map(([name, handler]) => [name, typeof handler])),
}));`,
    ],
    { env: { PATH: process.env.PATH, ...cacheEnv } },
  );

  expect(JSON.parse(stdout)).toEqual({
    before: { slot: "function" },
    after: { store: "function" },
    handlers: { FetchCache: "function", DefaultCache: "object", RemoteCache: "object" },
  });
});

test("the server preload withholds a response's shared-cache lifetime from Cloud CDN", async () => {
  const { stdout } = await execFileAsync(
    process.execPath,
    [
      "--input-type=module",
      "-e",
      `import http from "node:http";
await import(${JSON.stringify(join(dir, "server-preload.mjs"))});
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
      `await import(${JSON.stringify(join(dir, "server-preload.mjs"))});
${registerNextServer(`{ cacheHandler: "/app/mine.cjs" }`)}`,
    ],
    { env: { PATH: process.env.PATH } },
  );

  await expect(run).rejects.toThrow("cacheHandler");
});

test("a process running the server preload that runs no Next server listens without building its host", async () => {
  const { stdout } = await execFileAsync(
    process.execPath,
    [
      "--input-type=module",
      "-e",
      `import net from "node:net";
await import(${JSON.stringify(join(dir, "server-preload.mjs"))});
const server = net.createServer();
await new Promise((done) => server.listen(0, "127.0.0.1", done));
server.close();
process.stdout.write("listened");`,
    ],
    {
      env: { PATH: process.env.PATH, OCEL_CDN_URL_MAP: "m" },
      cwd: tmpdir(),
    },
  );

  expect(stdout).toBe("listened");
});

test("the runtime directory ships sharp built for the Linux x64 Cloud Run runs", async () => {
  expect(await shippedFiles()).toEqual(
    expect.arrayContaining([
      "node_modules/sharp/package.json",
      expect.stringMatching(/^node_modules\/@img\/sharp-linux-x64\/lib\/sharp-linux-x64.*\.node$/),
      expect.stringMatching(/^node_modules\/@img\/sharp-libvips-linux-x64\/lib\/libvips-cpp\.so/),
    ]),
  );
});

test("no file in the runtime directory contains a path of the checkout it was built in", async () => {
  const checkout = resolve(pkgDir, "..", "..", "..");
  const leaking: string[] = [];
  for (const file of await shippedFiles()) {
    if ((await readFile(join(dir, file), "utf8")).includes(checkout)) leaking.push(file);
  }
  expect(leaking).toEqual([]);
});

test("the entrypoint serves a Next app on the port Cloud Run names", async () => {
  const projectDir = join(dist, "project");
  const fixture = await writeNextProjectFixture(projectDir);
  const launcher = join(projectDir, "__next_launcher.cjs");
  await writeFile(
    launcher,
    withLauncherConfig(
      `module.exports = { async handler(req, res) { res.end("rendered"); } };\n`,
      fixture.nextConfig,
    ),
  );
  const port = await freePort();

  const child = spawn(process.execPath, [join(dir, "entrypoint.mjs")], {
    cwd: projectDir,
    env: { PATH: process.env.PATH, OCEL_HANDLER: launcher, PORT: String(port) },
    stdio: ["ignore", "inherit", "inherit"],
  });
  children.push(child);

  expect(await answer(port)).toBe("rendered");
});

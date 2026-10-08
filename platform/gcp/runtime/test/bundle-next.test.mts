import { type ChildProcess, execFile, spawn } from "node:child_process";
import { mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import http from "node:http";
import { isBuiltin } from "node:module";
import net from "node:net";
import { tmpdir } from "node:os";
import { join, relative, resolve } from "node:path";
import { promisify } from "node:util";
import { writeNextProjectFixture } from "@framework/next-runtime/test-support/next-project-fixture";
import { init, parse } from "es-module-lexer";
import { afterAll, afterEach, beforeAll, expect, test } from "vitest";
import { isRefreshTaskSignedBy, refreshSignatureHeader } from "../src/next/refresh-signature.mjs";

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
  OCEL_ISR_BUCKET: "bucket",
  OCEL_ISR_OBJECT_PREFIX: "cache/app",
  OCEL_ISR_PREFIX: "app",
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
      `process.env.OCEL_REFRESH_SECRET = "s1";
await import(${JSON.stringify(join(dir, "server-preload.mjs"))});
const handlers = globalThis[Symbol.for("@next/cache-handlers")];
const before = { slot: typeof globalThis[Symbol.for("ocel.next.host.v1")], secret: process.env.OCEL_REFRESH_SECRET ?? null };
${registerNextServer("{ cacheHandlers: {} }")}
const host = globalThis[Symbol.for("ocel.next.host.v1")];
process.stdout.write(JSON.stringify({
  before,
  after: { store: typeof host.newCacheStore, secret: process.env.OCEL_REFRESH_SECRET ?? null },
  handlers: Object.fromEntries(Object.entries(handlers ?? {}).map(([name, handler]) => [name, typeof handler])),
}));`,
    ],
    { env: { PATH: process.env.PATH, ...cacheEnv } },
  );

  expect(JSON.parse(stdout)).toEqual({
    before: { slot: "function", secret: "s1" },
    after: { store: "function", secret: null },
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

test("a process running the server preload that runs no Next server listens and keeps its environment", async () => {
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
process.stdout.write(process.env.OCEL_REFRESH_SECRET ?? "");`,
    ],
    {
      env: { PATH: process.env.PATH, OCEL_REFRESH_SECRET: "s1", OCEL_CDN_URL_MAP: "m" },
      cwd: tmpdir(),
    },
  );

  expect(stdout).toBe("s1");
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

interface QueuedTask {
  task: { httpRequest: { body: string; headers: Record<string, string> } };
}

async function fakeCloudTasks(): Promise<{
  origin: string;
  bodies: QueuedTask[];
  server: http.Server;
}> {
  const bodies: QueuedTask[] = [];
  const server = http.createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => {
      body += chunk;
    });
    req.on("end", () => {
      bodies.push(JSON.parse(body));
      res.setHeader("Content-Type", "application/json");
      res.end("{}");
    });
  });
  await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
  const { port } = server.address() as net.AddressInfo;
  return { origin: `http://127.0.0.1:${port}`, bodies, server };
}

const signedBySecret = (task: QueuedTask) =>
  isRefreshTaskSignedBy(
    "s1",
    Buffer.from(task.task.httpRequest.body, "base64"),
    task.task.httpRequest.headers[refreshSignatureHeader],
  );

const decodedTask = (task: QueuedTask) =>
  JSON.parse(Buffer.from(task.task.httpRequest.body, "base64").toString()) as {
    isrPrefix: string;
    refresh: { url: string; key: string; lastModified: number };
  };

const refreshEnv = (origin: string) => ({
  OCEL_ISR_PREFIX: "prod/shop/web/r1/isr",
  OCEL_REFRESH_URL: `${origin}/_ocel/refresh`,
  OCEL_REFRESH_QUEUE: "projects/p/locations/r/queues/q",
  OCEL_REFRESH_ACCOUNT: "ocel-production-refresh@p.iam.gserviceaccount.com",
  OCEL_REFRESH_SECRET: "s1",
  OCEL_TASKS_ENDPOINT: origin,
});

const staleLauncher = (log: string) => `const { appendFileSync } = require("node:fs");
module.exports = {
  async handler(req, res) {
    if (req.headers["x-ocel-refresh"]) {
      await new Promise((done) => setTimeout(done, 200));
      appendFileSync(${JSON.stringify(log)}, "refreshed " + req.url + "\\n");
      res.end("fresh");
      return;
    }
    req.headers[Symbol.for("ocel.next.stale-entry.v2")] = { key: "blog", lastModified: 1000 };
    res.setHeader("x-nextjs-cache", "STALE");
    res.end("stale");
  },
};
`;

async function until(met: () => boolean, timeoutMs = 3_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!met()) {
    if (Date.now() > deadline) throw new Error(`nothing met the condition within ${timeoutMs}ms`);
    await new Promise((wait) => setTimeout(wait, 10));
  }
}

test("a stale page served by a Next service that refreshes by task queues one refresh task", async () => {
  const projectDir = join(dist, "stale-project");
  await writeNextProjectFixture(
    projectDir,
    {},
    { routes: { "/blog": { initialRevalidateSeconds: 60 } } },
  );
  const log = join(projectDir, "refreshes.log");
  await writeFile(log, "");
  const launcher = join(projectDir, "__next_launcher.cjs");
  await writeFile(launcher, staleLauncher(log));
  const manifest = join(projectDir, "next-route-table.json");
  await writeFile(
    manifest,
    JSON.stringify({
      entry: "bundle-0",
      buildId: "b1",
      basePath: "",
      pathnames: ["/blog"],
      routes: {
        beforeMiddleware: [],
        beforeFiles: [],
        afterFiles: [],
        dynamicRoutes: [],
        onMatch: [],
        fallback: [],
      },
      dispatch: { "/blog": { kind: "function", id: "bundle-0", entryKey: "/blog" } },
    }),
  );
  const tasks = await fakeCloudTasks();
  const port = await freePort();

  const child = spawn(process.execPath, [join(dir, "entrypoint.mjs")], {
    cwd: projectDir,
    env: {
      PATH: process.env.PATH,
      OCEL_HANDLER: launcher,
      PORT: String(port),
      ...refreshEnv(tasks.origin),
      OCEL_ORIGIN_DISPATCH: "1",
      OCEL_ORIGIN_SIGNED: "1",
      OCEL_NEXT_ROUTE_TABLE: manifest,
    },
    stdio: ["ignore", "inherit", "inherit"],
  });
  children.push(child);
  await answer(port);
  await writeFile(log, "");

  const res = await fetch(`http://127.0.0.1:${port}/blog`);

  expect(await res.text()).toBe("stale");
  await until(() => tasks.bodies.length > 0);
  tasks.server.close();
  expect(tasks.bodies).toHaveLength(1);
  expect(decodedTask(tasks.bodies[0]!)).toMatchObject({
    isrPrefix: "prod/shop/web/r1/isr",
    refresh: { url: "/blog", key: "blog", lastModified: 1000 },
  });
  expect(signedBySecret(tasks.bodies[0]!)).toBe(true);
  expect(await readFile(log, "utf8")).toBe("");
});

test("the entrypoint serves a Next app on the port Cloud Run names", async () => {
  const projectDir = join(dist, "project");
  await writeNextProjectFixture(projectDir);
  const launcher = join(projectDir, "__next_launcher.cjs");
  await writeFile(
    launcher,
    `module.exports = { async handler(req, res) { res.end("rendered"); } };\n`,
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

const partiallyStaticLauncher = (log: string) => `const { appendFileSync } = require("node:fs");
const Handler = globalThis[Symbol.for("@next/cache-handlers")].FetchCache;
const page = { kind: "APP_PAGE", html: "<p>old</p>", status: 200, headers: {} };
module.exports = {
  async handler(req, res) {
    if (req.headers["x-ocel-refresh"]) {
      await new Promise((done) => setTimeout(done, 200));
      appendFileSync(${JSON.stringify(log)}, "refreshed " + req.url + "\\n");
      res.end("fresh");
      return;
    }
    if (req.headers["x-seed"]) {
      await new Handler({}).set("/blog", page, { cacheControl: { revalidate: 1 } });
      while (!(await new Handler({}).get("/blog", { kind: "APP_PAGE" }))) {
        await new Promise((done) => setTimeout(done, 10));
      }
      res.end("seeded");
      return;
    }
    const entry = await new Handler({ _requestHeaders: req.headers }).get("/blog", {
      kind: "APP_PAGE",
    });
    res.end(entry === null ? "no entry" : "entry");
  },
};
`;

test("a stale RSC navigation to a partially static page is answered without the entry and queues one refresh task", async () => {
  const projectDir = join(dist, "ppr-project");
  await writeNextProjectFixture(
    projectDir,
    { cacheComponents: true },
    { routes: { "/blog": { initialRevalidateSeconds: 60, renderingMode: "PARTIALLY_STATIC" } } },
  );
  const log = join(projectDir, "refreshes.log");
  await writeFile(log, "");
  const launcher = join(projectDir, "__next_launcher.cjs");
  await writeFile(launcher, partiallyStaticLauncher(log));
  const manifest = join(projectDir, "next-route-table.json");
  await writeFile(
    manifest,
    JSON.stringify({
      entry: "bundle-0",
      buildId: "b1",
      basePath: "",
      pathnames: ["/blog"],
      routes: {
        beforeMiddleware: [],
        beforeFiles: [],
        afterFiles: [],
        dynamicRoutes: [],
        onMatch: [],
        fallback: [],
      },
      dispatch: { "/blog": { kind: "function", id: "bundle-0", entryKey: "/blog" } },
    }),
  );
  const tasks = await fakeCloudTasks();
  const port = await freePort();
  const child = spawn(process.execPath, [join(dir, "entrypoint.mjs")], {
    cwd: projectDir,
    env: {
      PATH: process.env.PATH,
      OCEL_HANDLER: launcher,
      PORT: String(port),
      ...refreshEnv(tasks.origin),
      OCEL_ORIGIN_DISPATCH: "1",
      OCEL_ORIGIN_SIGNED: "1",
      OCEL_NEXT_ROUTE_TABLE: manifest,
    },
    stdio: ["ignore", "inherit", "inherit"],
  });
  children.push(child);
  await answer(port);
  const seeded = await fetch(`http://127.0.0.1:${port}/blog`, { headers: { "x-seed": "1" } });
  expect(await seeded.text()).toBe("seeded");
  await new Promise((wait) => setTimeout(wait, 1_100));
  await writeFile(log, "");

  const res = await fetch(`http://127.0.0.1:${port}/blog`, { headers: { RSC: "1" } });

  expect(await res.text()).toBe("no entry");
  await until(() => tasks.bodies.length > 0);
  tasks.server.close();
  expect(tasks.bodies).toHaveLength(1);
  const queued = decodedTask(tasks.bodies[0]!);
  expect(queued.isrPrefix).toBe("prod/shop/web/r1/isr");
  expect(queued.refresh).toMatchObject({ url: "/blog", key: "blog" });
  expect(typeof queued.refresh.lastModified).toBe("number");
  expect(signedBySecret(tasks.bodies[0]!)).toBe(true);
  expect(await readFile(log, "utf8")).toBe("");
});

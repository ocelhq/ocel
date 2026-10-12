import { mkdtemp, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test } from "vitest";
import { installNextHost } from "../src/host.mjs";
import { notePurge } from "../src/purge-signal.mjs";
import { writeNextProjectFixture } from "../test-support/next-project-fixture.mjs";

const launcherModule = `module.exports = {
  async handler(req, res) {
    if (req.url === "/tagged") {
      const copied = Object.assign({}, req.headers);
      copied[Symbol.for("ocel.next.origin-cache-tags.v1")].push("products", "_N_T_/products");
      res.setHeader("content-type", "text/html");
      res.end("tagged");
      return;
    }
    if (req.url === "/purged") {
      globalThis.__announceTestPurge();
      res.end("purged");
      return;
    }
    res.end(JSON.stringify(req.headers[Symbol.for("ocel.next.served-route")] ?? null));
  },
};
`;

let controlServer: net.Server;
const controlConns = new Set<net.Socket>();
let dir: string;
let ready: Promise<number>;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-next-fresh-only-"));

  const sockPath = join(dir, "control.sock");
  ready = new Promise((resolve) => {
    controlServer = net.createServer((conn) => {
      controlConns.add(conn);
      conn.on("close", () => controlConns.delete(conn));
      let buffer = "";
      conn.on("data", (chunk) => {
        buffer += chunk.toString();
        for (let end = buffer.indexOf("\n"); end >= 0; end = buffer.indexOf("\n")) {
          const message = JSON.parse(buffer.slice(0, end));
          buffer = buffer.slice(end + 1);
          if (message.type === "server-ready") resolve(message.payload.httpPort);
        }
      });
    });
  });
  await new Promise<void>((resolve) => controlServer.listen(sockPath, resolve));

  const projectDir = join(dir, "project");
  await writeNextProjectFixture(
    projectDir,
    {},
    { routes: { "/blog": { initialRevalidateSeconds: 60 } } },
  );
  const launcherPath = join(projectDir, "__next_launcher.cjs");
  await writeFile(launcherPath, launcherModule);

  delete process.env.OCEL_ORIGIN_DISPATCH;
  installNextHost({
    newCacheStore: async () => ({
      readEntry: async () => null,
      writeEntry: async () => {},
      readFetch: async () => null,
      writeFetch: async () => {},
    }),
    newUseCacheStore: async () => ({
      readEntry: async () => null,
      writeEntry: async () => {},
      readTagSnapshot: async () => ({ status: "unusable" }),
      writeTag: async () => true,
    }),
  });
  process.env.OCEL_EDGE_CACHE = "all";
  process.env.OCEL_CACHE_TAG_PURGE = "1";
  process.env.OCEL_ISR_PREFIX = "prod/shop/web/r0a1b2c3d/isr";
  (globalThis as Record<string, unknown>).__announceTestPurge = () =>
    notePurge("products", { expired: 1 }, 1);
  process.env.OCEL_CONTROL_SOCKET = sockPath;
  process.env.OCEL_HANDLER = launcherPath;
});

afterAll(async () => {
  delete process.env.OCEL_EDGE_CACHE;
  delete process.env.OCEL_CACHE_TAG_PURGE;
  delete process.env.OCEL_ISR_PREFIX;
  delete (globalThis as Record<string, unknown>).__announceTestPurge;
  for (const c of controlConns) c.end();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
});

test("a fresh-only origin tells the cache handler the revalidate window of the route it serves", async () => {
  await import("../src/entrypoint.mjs");
  const port = await ready;

  const res = await fetch(`http://127.0.0.1:${port}/blog`);

  expect(JSON.parse(await res.text())).toEqual({ revalidate: 60 });
});

test("a function that dispatches no path itself tags its responses for a cache that stores every one", async () => {
  await import("../src/entrypoint.mjs");
  const port = await ready;

  const res = await fetch(`http://127.0.0.1:${port}/tagged`);

  expect(res.headers.get("cache-tag")).toBe("r0a1b2c3d|_N_T_/products,r0a1b2c3d|products");
  expect(res.headers.has("x-ocel-edge-cache")).toBe(false);
});

test("a function behind a cache that stores every response announces no purge in its response", async () => {
  await import("../src/entrypoint.mjs");
  const port = await ready;

  const res = await fetch(`http://127.0.0.1:${port}/purged`);

  expect(await res.text()).toBe("purged");
  expect(res.headers.has("x-ocel-purge")).toBe(false);
});

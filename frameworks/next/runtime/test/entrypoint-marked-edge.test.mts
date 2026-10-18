import { mkdtemp, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test } from "vitest";
import { installNextHost } from "../src/host.mjs";
import {
  withLauncherConfig,
  writeNextProjectFixture,
} from "../test-support/next-project-fixture.mjs";

const launcherModule = `module.exports = {
  async handler(req, res) {
    res.end(JSON.stringify(req.headers[Symbol.for("ocel.next.uncached-render")] ?? false));
  },
};
`;

let controlServer: net.Server;
const controlConns = new Set<net.Socket>();
let dir: string;
let ready: Promise<number>;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-next-marked-edge-"));

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
  const fixture = await writeNextProjectFixture(projectDir, {
    routes: { "/blog": { revalidate: 60 } },
  });
  const launcherPath = join(projectDir, "__next_launcher.cjs");
  await writeFile(launcherPath, withLauncherConfig(launcherModule, fixture.nextConfig));

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
  process.env.OCEL_EDGE_CACHE = "marked";
  process.env.OCEL_CONTROL_SOCKET = sockPath;
  process.env.OCEL_HANDLER = launcherPath;
});

afterAll(async () => {
  delete process.env.OCEL_EDGE_CACHE;
  for (const c of controlConns) c.end();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
});

test("an origin behind an edge that stores only marked responses calls a render no edge asked to store uncached", async () => {
  await import("../src/entrypoint.mjs");
  const port = await ready;

  const res = await fetch(`http://127.0.0.1:${port}/dynamic`);

  expect(JSON.parse(await res.text())).toBe(true);
});

test("an origin behind an edge that stores only marked responses does not call a render the edge asked to store uncached", async () => {
  await import("../src/entrypoint.mjs");
  const port = await ready;

  const res = await fetch(`http://127.0.0.1:${port}/dynamic`, {
    headers: { "x-ocel-edge-cache": "1" },
  });

  expect(JSON.parse(await res.text())).toBe(false);
});

test("an origin behind an edge that stores only marked responses does not call a route with a window uncached", async () => {
  await import("../src/entrypoint.mjs");
  const port = await ready;

  const res = await fetch(`http://127.0.0.1:${port}/blog`);

  expect(JSON.parse(await res.text())).toBe(false);
});

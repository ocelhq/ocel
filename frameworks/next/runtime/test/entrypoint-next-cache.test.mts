import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test } from "vitest";
import { installNextHost } from "../src/host.mjs";
import {
  withLauncherConfig,
  writeNextProjectFixture,
} from "../test-support/next-project-fixture.mjs";

const launcherModule = `module.exports = { async handler(req, res) {
  res.end(globalThis.__incrementalCache === undefined ? "no cache of ocel's" : "ocel's cache");
} };
`;

let dir: string;
let projectDir: string;
let port: number;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-next-own-cache-"));
  projectDir = join(dir, "project");
  const fixture = await writeNextProjectFixture(projectDir);
  const launcherPath = join(projectDir, "__next_launcher.cjs");
  await writeFile(launcherPath, withLauncherConfig(launcherModule, fixture.nextConfig));

  port = await freePort();
  process.env.PORT = String(port);
  process.env.OCEL_HANDLER = launcherPath;
  installNextHost({ bind: { host: "127.0.0.1", port } });
  await import("../src/entrypoint.mjs");
});

afterAll(async () => {
  delete process.env.PORT;
  delete process.env.OCEL_HANDLER;
  await rm(dir, { recursive: true, force: true });
});

test("a host that keeps no cache store leaves Next its own cache handlers", async () => {
  expect(await answer(port)).toBe("no cache of ocel's");
  expect((globalThis as Record<symbol, unknown>)[Symbol.for("@next/cache-handlers")]).toBe(
    undefined,
  );
});

test("with no cache store, the incremental cache a route module builds serves a prerender laid out under its serverDistDir", async () => {
  const serverDistDir = join(projectDir, ".next", "server");
  await mkdir(join(serverDistDir, "app", "blog"), { recursive: true });
  await writeFile(join(serverDistDir, "app", "blog", "first.html"), "<p>prerendered</p>");
  await writeFile(join(serverDistDir, "app", "blog", "first.rsc"), "rsc");
  await writeFile(
    join(serverDistDir, "app", "blog", "first.meta"),
    JSON.stringify({ status: 200, headers: { "x-next-cache-tags": "blog" } }),
  );
  const require = createRequire(join(projectDir, "package.json"));
  const { IncrementalCache } = require("next/dist/server/lib/incremental-cache/index.js");
  const { nodeFs } = require("next/dist/server/lib/node-fs-methods.js");
  const manifest = {
    version: 4,
    routes: {},
    dynamicRoutes: {},
    notFoundRoutes: [],
    preview: { previewModeId: "p" },
  };

  const cache = new IncrementalCache({
    fs: nodeFs,
    dev: false,
    requestHeaders: {},
    minimalMode: false,
    serverDistDir,
    flushToDisk: true,
    getPrerenderManifest: () => manifest,
  });
  const entry = await cache.get("/blog/first", { kind: "APP_PAGE", isRoutePPREnabled: false });

  expect(entry?.value).toMatchObject({ kind: "APP_PAGE", html: "<p>prerendered</p>" });
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

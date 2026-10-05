import { mkdtemp, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test } from "vitest";
import { installNextHost } from "../src/host.mjs";
import type { Refresh } from "../src/refresh.mjs";
import { writeNextProjectFixture } from "../test-support/next-project-fixture.mjs";

const launcherModule = `module.exports = {
  async handler(req, res) {
    req.headers[Symbol.for("ocel.next.stale-entry.v1")] = 1000;
    res.end(String(req.headers.purpose));
  },
};
`;

let controlServer: net.Server;
const controlConns = new Set<net.Socket>();
let dir: string;
let ready: Promise<number>;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-next-refresh-"));

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
  process.env.OCEL_CONTROL_SOCKET = sockPath;
  process.env.OCEL_HANDLER = launcherPath;
});

afterAll(async () => {
  for (const c of controlConns) c.end();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
});

test("a host that refreshes by request has a stale hit served as it is and refreshed through it", async () => {
  const scheduled: Refresh[] = [];
  installNextHost({
    scheduleRefresh: async (refresh) => {
      scheduled.push(refresh);
    },
  });

  await import("../src/entrypoint.mjs");
  const port = await ready;
  const res = await fetch(`http://127.0.0.1:${port}/blog`);

  expect(await res.text()).toBe("prefetch");
  expect(scheduled.map(({ url, lastModified }) => ({ url, lastModified }))).toEqual([
    { url: "/blog", lastModified: 1000 },
  ]);
});

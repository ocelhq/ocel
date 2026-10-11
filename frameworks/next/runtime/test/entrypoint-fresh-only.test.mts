import { mkdtemp, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test } from "vitest";
import { writeNextProjectFixture } from "../test-support/next-project-fixture.mjs";

const launcherModule = `module.exports = {
  async handler(req, res) {
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
  process.env.OCEL_ORIGIN_FRESH_ONLY = "1";
  process.env.OCEL_CONTROL_SOCKET = sockPath;
  process.env.OCEL_HANDLER = launcherPath;
});

afterAll(async () => {
  delete process.env.OCEL_ORIGIN_FRESH_ONLY;
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

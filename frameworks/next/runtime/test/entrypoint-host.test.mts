import { mkdtemp, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test } from "vitest";
import { installNextHost } from "../src/host.mjs";
import { writeNextProjectFixture } from "../test-support/next-project-fixture.mjs";

const launcherModule = `module.exports = { async handler(req, res) { res.end("rendered"); } };
`;

let controlServer: net.Server;
const controlConns = new Set<net.Socket>();
let dir: string;
let ready: Promise<number>;
const dispatchedFrom: string[] = [];

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-next-host-"));

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
  await writeNextProjectFixture(projectDir);
  const launcherPath = join(projectDir, "__next_launcher.cjs");
  await writeFile(launcherPath, launcherModule);

  process.env.OCEL_ORIGIN_DISPATCH = "1";
  process.env.OCEL_ORIGIN_SIGNED = "1";
  process.env.OCEL_CONTROL_SOCKET = sockPath;
  process.env.OCEL_HANDLER = launcherPath;
});

afterAll(async () => {
  for (const c of controlConns) c.end();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
});

test("an origin that dispatches serves through the dispatcher its host installed", async () => {
  installNextHost({
    newDispatchInvoke: async (localOrigin) => {
      dispatchedFrom.push(localOrigin);
      return (_req, res) => {
        res.end("dispatched");
      };
    },
  });

  await import("../src/entrypoint.mjs");
  const port = await ready;
  const res = await fetch(`http://127.0.0.1:${port}/`);

  expect(await res.text()).toBe("dispatched");
  expect(dispatchedFrom).toEqual([expect.stringMatching(/^http:\/\/127\.0\.0\.1:\d+$/)]);
});

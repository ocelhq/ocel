import { mkdtemp, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { GATE_INLINE_VAR, hashPassword } from "@platform/edge-contract/gate";
import { afterAll, beforeAll, expect, test } from "vitest";
import { previewModeId, writeNextProjectFixture } from "../test-support/next-project-fixture.mjs";

const launcherModule = `module.exports = { async handler(req, res) { res.setHeader("x-heard-revalidate", req.headers["x-prerender-revalidate"] ?? ""); res.end("rendered"); } };
`;

let controlServer: net.Server;
const controlConns = new Set<net.Socket>();
let dir: string;
let ready: Promise<number>;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-next-gate-"));

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

  process.env.OCEL_CONTROL_SOCKET = sockPath;
  process.env.OCEL_HANDLER = launcherPath;
  process.env[GATE_INLINE_VAR] = JSON.stringify({
    key: "k",
    passwordMac: await hashPassword("k", "pw"),
  });
});

afterAll(async () => {
  for (const c of controlConns) c.end();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
});

test("a gated origin admits the HEAD a pages route's revalidate sends with its build's preview mode id, and no GET carrying it", async () => {
  await import("../src/entrypoint.mjs");
  const port = await ready;
  const headers = { "x-prerender-revalidate": previewModeId };

  const revalidated = await fetch(`http://127.0.0.1:${port}/blog`, { method: "HEAD", headers });
  expect(revalidated.status).toBe(200);
  expect(revalidated.headers.get("x-heard-revalidate")).toBe(previewModeId);

  const read = await fetch(`http://127.0.0.1:${port}/blog`, { headers });
  expect(read.status).toBe(401);
});

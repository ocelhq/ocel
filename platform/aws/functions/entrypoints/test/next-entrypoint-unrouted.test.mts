import net from "node:net";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test, vi } from "vitest";
import { writeNextProjectFixture } from "./next-project-fixture.mjs";

const launcherModule = `module.exports = { async handler(req, res) { res.end("ok"); } };
`;

let controlServer: net.Server;
const controlConns = new Set<net.Socket>();
let dir: string;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-next-unrouted-"));

  const sockPath = join(dir, "control.sock");
  controlServer = net.createServer((conn) => {
    controlConns.add(conn);
    conn.on("close", () => controlConns.delete(conn));
    conn.resume();
  });
  await new Promise<void>((resolve) => controlServer.listen(sockPath, resolve));

  const projectDir = join(dir, "project");
  await writeNextProjectFixture(projectDir);
  const launcherPath = join(projectDir, "__next_launcher.cjs");
  await writeFile(launcherPath, launcherModule);

  process.env.OCEL_EDGE_KIND = "native";
  delete process.env.OCEL_ROUTING_MANIFEST;
  process.env.OCEL_CONTROL_SOCKET = sockPath;
  process.env.OCEL_HANDLER = launcherPath;
});

afterAll(async () => {
  for (const c of controlConns) c.end();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
});

test("an origin that must route but was packed no manifest dies at boot", async () => {
  const exited = new Promise<number | undefined>((resolve) => {
    vi.spyOn(process, "exit").mockImplementation(((code?: number) => {
      resolve(code);
      return undefined as never;
    }) as never);
  });
  const reported: unknown[] = [];
  vi.spyOn(console, "error").mockImplementation((...args) => {
    reported.push(args.join(" "));
  });

  await import("../src/next/entrypoint.mjs");

  expect(await exited).toBe(1);
  expect(reported.join("\n")).toMatch(/OCEL_ROUTING_MANIFEST/);
});

import { mkdtemp, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  edgeCacheHeader,
  postponedLengthHeader,
  pprShellHeader,
} from "@platform/edge-contract/edge-cache";
import { afterAll, beforeAll, expect, test } from "vitest";
import { writeNextProjectFixture } from "../test-support/next-project-fixture.mjs";

const launcherModule = `module.exports = {
  async handler(req, res, ctx) {
    const meta = ctx.requestMeta;
    res.setHeader("x-minimal-mode", String(meta.minimalMode === true));
    if (meta.onCacheEntry) {
      const postponed = req.url.startsWith("/static") ? undefined : "STATE-é";
      const finished = await meta.onCacheEntry({ value: { kind: "APP_PAGE", postponed } }, { url: req.url });
      res.setHeader("x-finished", String(finished));
    }
    res.setHeader("cache-control", "s-maxage=60, stale-while-revalidate=300");
    res.setHeader("content-length", "11");
    res.write("<html>");
    res.end("shell");
  },
};
`;

let controlServer: net.Server;
const controlConns = new Set<net.Socket>();
let dir: string;
let ready: Promise<number>;
let port: number;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-next-ppr-shell-"));

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
  await writeNextProjectFixture(projectDir, {}, { routes: {} });
  const launcherPath = join(projectDir, "__next_launcher.cjs");
  await writeFile(launcherPath, launcherModule);

  delete process.env.OCEL_ORIGIN_DISPATCH;
  process.env.OCEL_CONTROL_SOCKET = sockPath;
  process.env.OCEL_HANDLER = launcherPath;

  await import("../src/entrypoint.mjs");
  port = await ready;
});

afterAll(async () => {
  for (const c of controlConns) c.end();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
});

const shellHeaders = { [edgeCacheHeader]: "1", [pprShellHeader]: "1" };

test("an edge asking for a PPR shell gets it from a minimal-mode render, framed after its postponed state", async () => {
  const res = await fetch(`http://127.0.0.1:${port}/blog`, { headers: shellHeaders });

  const state = "STATE-é";
  expect(res.headers.get("x-minimal-mode")).toBe("true");
  expect(res.headers.get("x-finished")).toBe("false");
  expect(res.headers.get(postponedLengthHeader)).toBe(String(Buffer.byteLength(state)));
  expect(res.headers.get("cache-control")).toBe("s-maxage=60, stale-while-revalidate=300");
  expect(await res.text()).toBe(`${state}<html>shell`);
});

test("a shell whose page did not postpone is answered as it is", async () => {
  const res = await fetch(`http://127.0.0.1:${port}/static`, { headers: shellHeaders });

  expect(res.headers.has(postponedLengthHeader)).toBe(false);
  expect(await res.text()).toBe("<html>shell");
});

test("a request that does not ask for the shell renders as it always did", async () => {
  const res = await fetch(`http://127.0.0.1:${port}/blog`, {
    headers: { [edgeCacheHeader]: "1" },
  });

  expect(res.headers.get("x-minimal-mode")).toBe("false");
  expect(res.headers.has(postponedLengthHeader)).toBe(false);
  expect(await res.text()).toBe("<html>shell");
});

test("a shell is only framed for a GET from the edge cache", async () => {
  const res = await fetch(`http://127.0.0.1:${port}/blog`, { headers: { [pprShellHeader]: "1" } });

  expect(res.headers.get("x-minimal-mode")).toBe("false");
  expect(await res.text()).toBe("<html>shell");
});

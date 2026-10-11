import http from "node:http";
import type { AddressInfo } from "node:net";
import { postponedLengthHeader } from "@platform/edge-contract/edge-cache";
import { afterEach, expect, test } from "vitest";
import { framePprShell } from "../src/ppr-shell.mjs";

const state = "STATE-é";
let server: http.Server | undefined;

afterEach(async () => {
  await new Promise<void>((resolve) => (server ? server.close(() => resolve()) : resolve()));
  server = undefined;
});

async function served(respond: (res: http.ServerResponse) => void): Promise<string> {
  server = http.createServer(async (_req, res) => {
    const meta = framePprShell(res);
    await meta.onCacheEntry({ value: { postponed: state } });
    respond(res);
  });
  await new Promise<void>((resolve) => server!.listen(0, "127.0.0.1", resolve));
  return `http://127.0.0.1:${(server.address() as AddressInfo).port}/`;
}

test("a shell whose length is set in writeHead's headers is framed whole", async () => {
  const url = await served((res) => {
    res.writeHead(200, { "content-type": "text/html", "content-length": "11" });
    res.end("<html>shell");
  });

  const res = await fetch(url);

  expect(res.headers.get(postponedLengthHeader)).toBe(String(Buffer.byteLength(state)));
  expect(await res.text()).toBe(`${state}<html>shell`);
});

test("a shell whose length is set in writeHead's raw header list is framed whole", async () => {
  const url = await served((res) => {
    res.writeHead(200, "OK", ["content-type", "text/html", "Content-Length", "11"]);
    res.end("<html>shell");
  });

  const res = await fetch(url);

  expect(await res.text()).toBe(`${state}<html>shell`);
});

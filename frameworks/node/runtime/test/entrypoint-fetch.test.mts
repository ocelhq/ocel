import { mkdtemp, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test } from "vitest";

const appModule = `export default {
  fetch: (request) => new Response("from fetch " + new URL(request.url).pathname),
};
`;

const messages: { type: string; payload: any }[] = [];
const controlConns = new Set<net.Socket>();
let controlServer: net.Server;
let dir: string;
let port: number;

function serverReady(timeoutMs = 5000): Promise<number> {
  const deadline = Date.now() + timeoutMs;
  return new Promise((resolve, reject) => {
    const poll = (): void => {
      const ready = messages.find((m) => m.type === "server-ready");
      if (ready) {
        resolve(ready.payload.httpPort);
      } else if (Date.now() > deadline) {
        reject(
          new Error(`nothing reported a server; messages so far: ${JSON.stringify(messages)}`),
        );
      } else {
        setTimeout(poll, 25);
      }
    };
    poll();
  });
}

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-entry-fetch-"));

  const sockPath = join(dir, "control.sock");
  controlServer = net.createServer((conn) => {
    controlConns.add(conn);
    conn.on("close", () => controlConns.delete(conn));
    let buf = "";
    conn.on("data", (d) => {
      buf += d.toString();
      let idx: number;
      while ((idx = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, idx);
        buf = buf.slice(idx + 1);
        if (line.trim()) messages.push(JSON.parse(line));
      }
    });
  });
  await new Promise<void>((resolve) => controlServer.listen(sockPath, resolve));

  const handler = join(dir, "app.mjs");
  await writeFile(handler, appModule);

  process.env.OCEL_CONTROL_SOCKET = sockPath;
  process.env.OCEL_HANDLER = handler;
  await import("../src/entrypoint.mjs");
  port = await serverReady();
});

afterAll(async () => {
  for (const c of controlConns) c.end();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
  delete process.env.OCEL_CONTROL_SOCKET;
  delete process.env.OCEL_HANDLER;
});

test("an app exporting only a fetch handler is served rather than waited on", async () => {
  const response = await fetch(`http://127.0.0.1:${port}/web`);

  expect(response.status).toBe(200);
  expect(await response.text()).toBe("from fetch /web");
});

import { type ChildProcess, execFile, spawn } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import { networkInterfaces, tmpdir } from "node:os";
import { join, relative, resolve } from "node:path";
import { promisify } from "node:util";
import { afterAll, afterEach, beforeAll, expect, test } from "vitest";

const execFileAsync = promisify(execFile);
const pkgDir = resolve(import.meta.dirname, "..");

const children: ChildProcess[] = [];
const dirs: string[] = [];

let serveBundle: string;

beforeAll(async () => {
  const dist = await mkdtemp(join(tmpdir(), "ocel-serve-"));
  dirs.push(dist);
  await execFileAsync("bun", ["scripts/bundle-serve.mjs", relative(pkgDir, dist)], { cwd: pkgDir });
  serveBundle = join(dist, "serve.mjs");
}, 120_000);

afterAll(async () => {
  for (const dir of dirs.splice(0)) await rm(dir, { recursive: true, force: true });
});

afterEach(() => {
  for (const child of children.splice(0)) child.kill("SIGKILL");
});

async function appDir(): Promise<string> {
  const dir = await mkdtemp(join(import.meta.dirname, "serve-"));
  dirs.push(dir);
  return dir;
}

function freePort(): Promise<number> {
  return new Promise((done) => {
    const probe = net.createServer();
    probe.listen({ host: "127.0.0.1", port: 0 }, () => {
      const { port } = probe.address() as net.AddressInfo;
      probe.close(() => done(port));
    });
  });
}

async function reachable(port: number): Promise<number> {
  for (let attempt = 0; attempt < 200; attempt++) {
    try {
      await fetch(`http://127.0.0.1:${port}/ping`);
      return port;
    } catch {
      await new Promise((wait) => setTimeout(wait, 25));
    }
  }
  throw new Error(`nothing answered on port ${port}`);
}

function start(handler: string, port: number, stdio: "inherit" | "pipe"): ChildProcess {
  const child = spawn(process.execPath, [serveBundle], {
    env: { ...process.env, OCEL_HANDLER: handler, PORT: String(port) },
    stdio: ["ignore", stdio, stdio],
  });
  children.push(child);
  return child;
}

async function serve(handler: string): Promise<number> {
  const port = await freePort();
  start(handler, port, "inherit");
  return reachable(port);
}

async function handlerFile(body: string): Promise<string> {
  const file = join(await appDir(), "app.mjs");
  await writeFile(file, body);
  return file;
}

test("serves an express app on the port it was given", async () => {
  const handler = await handlerFile(`import express from "express";
const app = express();
app.get("/hello", (_req, res) => res.send("from express"));
export default app;
`);

  const port = await serve(handler);
  const response = await fetch(`http://127.0.0.1:${port}/hello`);

  expect(await response.text()).toBe("from express");
});

test("serves a (req, res) handler on the port it was given", async () => {
  const handler = await handlerFile(`export default (req, res) => res.end("from node");
`);

  const port = await serve(handler);
  const response = await fetch(`http://127.0.0.1:${port}/anything`);

  expect(await response.text()).toBe("from node");
});

test("serves a fetch handler on the port it was given", async () => {
  const handler = await handlerFile(`export default {
  fetch: (request) => new Response("from fetch " + new URL(request.url).pathname),
};
`);

  const port = await serve(handler);
  const response = await fetch(`http://127.0.0.1:${port}/web`);

  expect(await response.text()).toBe("from fetch /web");
});

const routable = Object.values(networkInterfaces())
  .flat()
  .find((entry) => entry?.family === "IPv4" && !entry.internal)?.address;

test.skipIf(!routable)(
  "answers on the address the platform routes to, not only loopback",
  async () => {
    const handler = await handlerFile(`export default (req, res) => res.end("reachable");
`);

    const port = await serve(handler);
    const response = await fetch(`http://${routable}:${port}/`);

    expect(await response.text()).toBe("reachable");
  },
);

test("refuses to boot without the port to serve on", async () => {
  const handler = await handlerFile(`export default (req, res) => res.end("ok");
`);

  const failed = await execFileAsync(process.execPath, [serveBundle], {
    env: { ...process.env, OCEL_HANDLER: handler, PORT: "" },
  }).catch((err) => err);

  expect(failed.code).toBe(1);
  expect(failed.stderr).toContain("PORT");
});

test("refuses to boot without the handler to serve", async () => {
  const failed = await execFileAsync(process.execPath, [serveBundle], {
    env: { ...process.env, OCEL_HANDLER: "", PORT: String(await freePort()) },
  }).catch((err) => err);

  expect(failed.code).toBe(1);
  expect(failed.stderr).toContain("OCEL_HANDLER");
});

test("stops on SIGTERM where no control socket holds the other end", async () => {
  const handler = await handlerFile(`export default (req, res) => res.end("ok");
`);
  const port = await freePort();
  const child = start(handler, port, "pipe");
  let output = "";
  child.stdout?.on("data", (chunk) => {
    output += chunk;
  });
  child.stderr?.on("data", (chunk) => {
    output += chunk;
  });
  await reachable(port);

  const stopped = new Promise<NodeJS.Signals | null>((done) =>
    child.once("exit", (_code, signal) => done(signal)),
  );
  child.kill("SIGTERM");

  expect(await stopped).toBe("SIGTERM");
  expect(output).toBe("");
});

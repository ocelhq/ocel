import { type ChildProcess, spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import http from "node:http";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, expect, test } from "vitest";

const hostSrc = resolve(dirname(fileURLToPath(import.meta.url)), "../src/host.mts");

let dir: string;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-shutdown-"));
});
afterAll(async () => {
  await rm(dir, { recursive: true, force: true });
});

async function serving(
  body: string,
): Promise<{ child: ChildProcess; port: number; out: string[] }> {
  const file = join(dir, `child-${Math.random().toString(36).slice(2)}.mts`);
  await writeFile(
    file,
    `import { serveInvoke } from ${JSON.stringify(hostSrc)};\n${body}\n` +
      `await serveInvoke(invoke, (port) => console.log("port " + port));\n`,
  );
  const child = spawn(process.execPath, [file], {
    env: { ...process.env, OCEL_CONTROL_SOCKET: "" },
    stdio: ["ignore", "pipe", "inherit"],
  });
  const out: string[] = [];
  let buffer = "";
  const port = await new Promise<number>((resolvePort, reject) => {
    child.once("exit", (code) => reject(new Error(`the child exited ${code} before listening`)));
    child.stdout!.on("data", (chunk) => {
      buffer += chunk.toString();
      const lines = buffer.split("\n");
      buffer = lines.pop() ?? "";
      for (const line of lines) {
        out.push(line);
        if (line.startsWith("port ")) resolvePort(Number(line.slice(5)));
      }
    });
  });
  return { child, port, out };
}

function get(port: number): Promise<string> {
  return new Promise((resolveBody, reject) => {
    http
      .get({ host: "127.0.0.1", port, path: "/" }, (res) => {
        let body = "";
        res.on("data", (chunk) => (body += chunk));
        res.on("end", () => resolveBody(body));
      })
      .on("error", reject);
  });
}

test("a SIGTERM lets work handed to waitUntil after the response finish before the process exits", async () => {
  const { child, port, out } = await serving(
    `const invoke = (_req, res, ocel) => {\n` +
      `  ocel.waitUntil(new Promise((r) => setTimeout(() => { console.log("drained"); r(); }, 300)));\n` +
      `  res.end("ok");\n` +
      `};`,
  );

  expect(await get(port)).toBe("ok");
  child.kill("SIGTERM");
  const [code, signal] = await once(child, "exit");

  expect(out).toContain("drained");
  expect({ code, signal }).toEqual({ code: null, signal: "SIGTERM" });
});

test("a SIGTERM lets a request still being answered finish, and the work it hands waitUntil", async () => {
  const { child, port, out } = await serving(
    `const invoke = (_req, res, ocel) => {\n` +
      `  console.log("answering");\n` +
      `  setTimeout(() => {\n` +
      `    ocel.waitUntil(new Promise((r) => setTimeout(() => { console.log("drained"); r(); }, 200)));\n` +
      `    res.end("late");\n` +
      `  }, 300);\n` +
      `};`,
  );

  const answered = get(port);
  while (!out.includes("answering")) await new Promise((wait) => setTimeout(wait, 10));
  child.kill("SIGTERM");

  expect(await answered).toBe("late");
  const [code, signal] = await once(child, "exit");
  expect(out).toContain("drained");
  expect({ code, signal }).toEqual({ code: null, signal: "SIGTERM" });
});

test("a SIGTERM with no work outstanding exits at once", async () => {
  const { child, port } = await serving(`const invoke = (_req, res) => res.end("ok");`);

  expect(await get(port)).toBe("ok");
  const sent = performance.now();
  child.kill("SIGTERM");
  const [, signal] = await once(child, "exit");

  expect(signal).toBe("SIGTERM");
  expect(performance.now() - sent).toBeLessThan(1000);
});

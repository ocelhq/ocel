import { spawn } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { join } from "node:path";
import { afterAll, beforeAll, expect, it } from "vitest";

let child: ReturnType<typeof spawn>;
let port: number;
let dir: string;

function freePort(): Promise<number> {
  return new Promise((resolve) => {
    const server = createServer().listen(0, () => {
      const { port } = server.address() as { port: number };
      server.close(() => resolve(port));
    });
  });
}

beforeAll(async () => {
  dir = mkdtempSync(join(import.meta.dirname, ".serve-"));
  writeFileSync(
    join(dir, "app.js"),
    `export default { fetch(request) {
      if (new URL(request.url).pathname === "/throws") throw new Error("boom");
      return new Response("ok");
    } };\n`,
  );
  const serve = readFileSync(new URL("../files/serve.js", import.meta.url), "utf8");
  writeFileSync(join(dir, "index.js"), serve.replace("ENTRY", "./app.js"));
  port = await freePort();
  const { HOST: _host, ...inherited } = process.env;
  child = spawn(process.execPath, [join(dir, "index.js")], {
    env: { ...inherited, PORT: String(port) },
    stdio: ["ignore", "pipe", "pipe"],
  });
  await new Promise<void>((resolve) => child.stdout?.once("data", () => resolve()));
});

afterAll(() => {
  child?.kill();
  rmSync(dir, { recursive: true, force: true });
});

it("answers 500 to a request the app throws on, and keeps serving", async () => {
  const thrown = await fetch(`http://127.0.0.1:${port}/throws`);
  expect(thrown.status).toBe(500);
  const next = await fetch(`http://127.0.0.1:${port}/`);
  expect(await next.text()).toBe("ok");
});

it("listens on loopback alone unless HOST names another address", async () => {
  const { networkInterfaces } = await import("node:os");
  const outward = Object.values(networkInterfaces())
    .flat()
    .find((address) => address && address.family === "IPv4" && !address.internal);
  if (!outward) return;
  await expect(fetch(`http://${outward.address}:${port}/`)).rejects.toThrow();
});

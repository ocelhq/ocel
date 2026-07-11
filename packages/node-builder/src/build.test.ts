import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { afterAll, describe, expect, it } from "vitest";
import { buildApp, buildApps } from "./build";

const here = path.dirname(fileURLToPath(import.meta.url));
const fixtureDir = path.resolve(here, "../test/fixtures/express-app");

// Keep build output under the package so Node's upward node_modules lookup can
// resolve `express` at runtime; `.ocel` is gitignored repo-wide.
const outRoot = path.resolve(here, "../.ocel");

function freshOut(): string {
  mkdirSync(outRoot, { recursive: true });
  return mkdtempSync(path.join(outRoot, "test-"));
}

const dirs: string[] = [];
afterAll(() => {
  for (const d of dirs) rmSync(d, { recursive: true, force: true });
});

describe("buildApp", () => {
  it("produces the documented .func layout", async () => {
    const outDir = freshOut();
    dirs.push(outDir);

    const summary = await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const funcDir = path.join(outDir, "functions", "api.func");
    expect(existsSync(path.join(funcDir, "index.mjs"))).toBe(true);
    expect(existsSync(path.join(funcDir, "meta.json"))).toBe(true);
    expect(existsSync(path.join(funcDir, "src", "server.js"))).toBe(true);
    // JS helper is copied verbatim, TS entrypoint is transpiled next to it.
    expect(existsSync(path.join(funcDir, "src", "greeting.js"))).toBe(true);

    const meta = JSON.parse(readFileSync(path.join(funcDir, "meta.json"), "utf8"));
    expect(meta).toEqual({
      runtime: "nodejs20.x",
      handler: "index.handler",
      framework: "express",
    });

    expect(summary.name).toBe("api");
    expect(summary.runtime).toBe("nodejs20.x");
    expect(summary.handler).toBe("index.handler");
    expect(summary.framework).toBe("express");
    expect(summary.artifactPath).toBe(path.join("functions", "api.func"));
  });

  it("preserves the module tree instead of emitting a single bundle", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const funcDir = path.join(outDir, "functions", "api.func");
    // Transpiled entrypoint still imports its dependencies (not inlined).
    const server = readFileSync(path.join(funcDir, "src", "server.js"), "utf8");
    expect(server).toContain('from "express"');
    expect(server).toContain("./greeting.js");
    // express itself was traced into the artifact, not merged into server.js.
    expect(existsSync(path.join(funcDir, "node_modules", "express"))).toBe(true);
  });

  it("emits an invocable handler that answers a Lambda Function URL (v2) event", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const funcDir = path.join(outDir, "functions", "api.func");
    const { handler } = await import(path.join(funcDir, "index.mjs"));

    const event = {
      version: "2.0",
      routeKey: "$default",
      rawPath: "/hello/world",
      rawQueryString: "",
      headers: { host: "example.com" },
      requestContext: {
        http: {
          method: "GET",
          path: "/hello/world",
          protocol: "HTTP/1.1",
          sourceIp: "127.0.0.1",
        },
      },
      isBase64Encoded: false,
    };

    const res = await handler(event, {});
    expect(res.statusCode).toBe(200);
    expect(JSON.parse(res.body)).toEqual({ message: "hello, world" });
  });

  it("throws naming the candidates when no entrypoint resolves", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    const emptyDir = mkdtempSync(path.join(tmpdir(), "nb-empty-"));
    dirs.push(emptyDir);

    await expect(
      buildApp({ name: "api", cwd: emptyDir }, { outDir }),
    ).rejects.toThrow(/src\/server\.ts/);
  });

  it("honors an explicit entrypoint override", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    const summary = await buildApp(
      { name: "api", cwd: fixtureDir, entrypoint: "src/server.ts" },
      { outDir },
    );
    expect(summary.name).toBe("api");
  });
});

describe("buildApps", () => {
  it("returns one summary per app", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    const summaries = await buildApps(
      [
        { name: "api", cwd: fixtureDir },
        { name: "worker", cwd: fixtureDir, logicalName: "Worker" },
      ],
      { outDir },
    );
    expect(summaries.map((s) => s.name)).toEqual(["api", "worker"]);
    expect(summaries[1]!.logicalName).toBe("Worker");
  });
});

import { execFileSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { buildApp, buildApps } from "./build";

// Invoke a built handler in a REAL Node ESM process. vitest's own `await import`
// goes through Vite's bundler-style resolver, which resolves extensionless
// imports and would mask the raw-Node ERR_MODULE_NOT_FOUND we must guard.
function invokeInNode(indexMjs: string, event: unknown): { statusCode: number; body: string } {
  const script =
    `const { handler } = await import(${JSON.stringify(pathToFileURL(indexMjs).href)});\n` +
    `const res = await handler(${JSON.stringify(event)}, {});\n` +
    // Sentinels isolate the result from the app's own stdout (e.g. its listen log).
    `process.stdout.write("__RES__" + JSON.stringify(res) + "__END__");`;
  const out = execFileSync("node", ["--input-type=module", "-e", script], { encoding: "utf8" });
  const match = out.match(/__RES__([\s\S]*)__END__/);
  if (!match) throw new Error(`no handler result in output:\n${out}`);
  return JSON.parse(match[1] as string);
}

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

  it("rewrites extensionless relative specifiers, leaving bare/extensioned alone", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const server = readFileSync(
      path.join(outDir, "functions", "api.func", "src", "server.js"),
      "utf8",
    );
    // Extensionless relative file import -> gains the emitted extension.
    expect(server).toContain('"./lib/db.js"');
    expect(server).not.toMatch(/["']\.\/lib\/db["']/);
    // Extensionless relative directory import -> resolves to its index file.
    expect(server).toContain('"./config/index.js"');
    expect(server).not.toMatch(/["']\.\/config["']/);
    // Bare/package specifier untouched.
    expect(server).toContain('from "express"');
    // Already-extensioned relative specifier untouched.
    expect(server).toContain('"./greeting.js"');

    // A nested user module keeps its already-extensioned relative import as-is.
    const db = readFileSync(
      path.join(outDir, "functions", "api.func", "src", "lib", "db.js"),
      "utf8",
    );
    expect(db).toContain('"../greeting.js"');
  });

  it("rewrites extensionless relative imports in copied ESM deps (ocel-dist class)", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });
    const funcDir = path.join(outDir, "functions", "api.func");

    // Copied ESM dep: its extensionless internal import gains `.js`.
    const dep = readFileSync(path.join(funcDir, "node_modules", "fake-dep", "index.js"), "utf8");
    expect(dep).toContain('"./helper.js"');
    expect(dep).not.toMatch(/["']\.\/helper["']/);

    // Copied CJS dep: `require("./impl")` is left completely untouched.
    const cjs = readFileSync(path.join(funcDir, "node_modules", "cjs-dep", "index.js"), "utf8");
    expect(cjs).toContain('require("./impl")');
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

  it("inlines the adapter deps into index.mjs and is self-contained", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const funcDir = path.join(outDir, "functions", "api.func");
    // serverless-http is bundled into index.mjs, not left as a bare import the
    // user's app never traced — so the deployed Lambda can actually load it.
    const shim = readFileSync(path.join(funcDir, "index.mjs"), "utf8");
    expect(shim).not.toMatch(/from\s+["']serverless-http["']/);
    expect(shim).not.toMatch(/require\(\s*["']serverless-http["']\s*\)/);

    // Copy the artifact outside the repo (no ancestor node_modules) and invoke
    // it there: proves every runtime dep travels inside the .func.
    const isolated = mkdtempSync(path.join(tmpdir(), "nb-func-"));
    dirs.push(isolated);
    cpSync(funcDir, isolated, { recursive: true });

    const { handler } = await import(path.join(isolated, "index.mjs"));
    const res = await handler(
      {
        version: "2.0",
        rawPath: "/hello/isolated",
        rawQueryString: "",
        headers: { host: "example.com" },
        requestContext: { http: { method: "GET", path: "/hello/isolated" } },
        isBase64Encoded: false,
      },
      {},
    );
    expect(res.statusCode).toBe(200);
    expect(JSON.parse(res.body)).toEqual({ message: "hello, isolated" });
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

  it("clears orphaned .func dirs from a previous run", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApps([{ name: "old", cwd: fixtureDir }], { outDir });
    expect(existsSync(path.join(outDir, "functions", "old.func"))).toBe(true);

    await buildApps([{ name: "new", cwd: fixtureDir }], { outDir });
    expect(existsSync(path.join(outDir, "functions", "new.func"))).toBe(true);
    expect(existsSync(path.join(outDir, "functions", "old.func"))).toBe(false);
  });
});

// The embedded bundle is copied into a USER project's `.ocel/` and run with the
// user's node, where dev tools like esbuild/sucrase are not installed. It must
// therefore carry zero external runtime deps.
describe("embedded bundle self-containment", () => {
  const packageDir = path.resolve(here, "..");
  const bundle = path.join(packageDir, "dist", "node-builder.mjs");

  beforeAll(() => {
    execFileSync("node", ["build.mjs"], { cwd: packageDir, stdio: "inherit" });
  }, 120_000);

  it("has no bare imports of build-time-only deps", () => {
    const text = readFileSync(bundle, "utf8");
    for (const dep of ["esbuild", "sucrase", "serverless-http", "es-module-lexer"]) {
      const bare = new RegExp(
        `(?:from|import|require)\\s*\\(?\\s*["']${dep}["']`,
      );
      expect(text, `bundle must not carry a bare "${dep}" runtime import`).not.toMatch(bare);
    }
  });

  it("builds a valid, invocable .func when run outside the repo (no esbuild)", async () => {
    // Copy only the single .mjs to an isolated dir: nothing here resolves
    // esbuild/sucrase, reproducing a real user project + `ocel deploy`.
    const isoDir = mkdtempSync(path.join(tmpdir(), "nb-iso-"));
    dirs.push(isoDir);
    const isoBundle = path.join(isoDir, "node-builder.mjs");
    cpSync(bundle, isoBundle);

    const outDir = path.join(isoDir, "out");
    const request = JSON.stringify({ outDir, apps: [{ name: "api", cwd: fixtureDir }] });
    const stdout = execFileSync("node", [isoBundle, request], {
      cwd: isoDir,
      encoding: "utf8",
    });

    const summary = JSON.parse(stdout.trim().split("\n").pop() as string);
    expect(summary.functions[0].name).toBe("api");

    const funcDir = path.join(outDir, "functions", "api.func");
    // Hit the route whose handler transitively uses an extensionless relative
    // import (server.js -> ./lib/db.js -> ../greeting.js): fails with
    // ERR_MODULE_NOT_FOUND under raw Node unless the specifiers were rewritten.
    const res = invokeInNode(path.join(funcDir, "index.mjs"), {
      version: "2.0",
      rawPath: "/render/deployed",
      rawQueryString: "",
      headers: { host: "example.com" },
      requestContext: { http: { method: "GET", path: "/render/deployed" } },
      isBase64Encoded: false,
    });
    expect(res.statusCode).toBe(200);
    // server.js -> ./lib/db.js -> fake-dep (./helper.js) + cjs-dep (require).
    expect(JSON.parse(res.body)).toEqual({ message: ">[HELLO, DEPLOYED]", banner: "fixture" });
  });
});

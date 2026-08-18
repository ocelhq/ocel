import { execFileSync } from "node:child_process";
import {
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { buildApp, buildApps, detectApp, placeFile, writeBuildPlan } from "./build.js";
import { BUNDLE_HANDLER } from "./bundle.js";
import { sanitizeName } from "./detect.js";
import { BUILD_PLAN_FILE, SERVE_DESCRIPTOR_FILE, appOutDir } from "./layout.js";
import { artifactHash } from "./trace.js";

function importEntryInNode(entryMjs: string): { defaultType: string } {
  const script =
    `const mod = await import(${JSON.stringify(pathToFileURL(entryMjs).href)});\n` +
    `process.stdout.write("__RES__" + JSON.stringify({ defaultType: typeof mod.default }) + "__END__");`;
  const out = execFileSync("node", ["--input-type=module", "-e", script], { encoding: "utf8" });
  const match = out.match(/__RES__([\s\S]*)__END__/);
  if (!match) throw new Error(`no import result in output:\n${out}`);
  return JSON.parse(match[1] as string);
}

const here = path.dirname(fileURLToPath(import.meta.url));
const fixtureDir = path.resolve(here, "../../test/fixtures/express-app");

const outRoot = path.resolve(here, "../../.ocel");

function freshOut(): string {
  mkdirSync(outRoot, { recursive: true });
  return mkdtempSync(path.join(outRoot, "test-"));
}

const dirs: string[] = [];
afterAll(() => {
  for (const d of dirs) rmSync(d, { recursive: true, force: true });
});

beforeEach(() => vi.stubEnv("OCEL_BUILD_PREFER_TRACING", "1"));
afterEach(() => vi.unstubAllEnvs());

function appFuncDir(outDir: string, app: string): string {
  return path.join(appOutDir(outDir, app), "functions", "index.func");
}

describe("buildApp", () => {
  it("produces the documented .func layout", async () => {
    const outDir = freshOut();
    dirs.push(outDir);

    const [summary] = await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const funcDir = appFuncDir(outDir, "api");
    expect(existsSync(path.join(funcDir, "index.mjs"))).toBe(false);
    expect(existsSync(path.join(funcDir, "config.json"))).toBe(true);
    expect(existsSync(path.join(funcDir, "src", "server.js"))).toBe(true);
    expect(existsSync(path.join(funcDir, "src", "greeting.js"))).toBe(true);

    const config = JSON.parse(readFileSync(path.join(funcDir, "config.json"), "utf8"));
    expect(config).toEqual({
      runtime: "nodejs24.x",
      handler: "src/server.js",
      framework: "express",
      app: "api",
    });

    expect(summary.name).toBe("api");
    expect(summary.runtime).toBe("nodejs24.x");
    expect(summary.handler).toBe("src/server.js");
    expect(summary.framework).toBe("express");
    expect(summary.artifactPath).toBe(path.join("apps", "api", "functions", "index.func"));
    expect(summary.strategy).toBe("trace");
    expect(summary.entrypoint).toBeUndefined();
  });

  it("preserves the module tree instead of emitting a single bundle", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const funcDir = appFuncDir(outDir, "api");
    const server = readFileSync(path.join(funcDir, "src", "server.js"), "utf8");
    expect(server).toContain('from "express"');
    expect(server).toContain("./greeting.js");
    expect(existsSync(path.join(funcDir, "node_modules", "express"))).toBe(true);
  });

  it("strips types but preserves modern syntax verbatim (no downleveling)", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const server = readFileSync(
      path.join(appFuncDir(outDir, "api"), "src", "server.js"),
      "utf8",
    );
    expect(server).toContain("req.params?.name ?? ");
    expect(server).not.toContain("_optionalChain");
    expect(server).not.toContain("_nullishCoalesce");
    expect(server).not.toMatch(/:\s*(string|number|Request)\b/);
  });

  it("rewrites extensionless relative specifiers, leaving bare/extensioned alone", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const server = readFileSync(
      path.join(appFuncDir(outDir, "api"), "src", "server.js"),
      "utf8",
    );
    expect(server).toContain('"./lib/db.js"');
    expect(server).not.toMatch(/["']\.\/lib\/db["']/);
    expect(server).toContain('"./config/index.js"');
    expect(server).not.toMatch(/["']\.\/config["']/);
    expect(server).toContain('from "express"');
    expect(server).toContain('"./greeting.js"');

    const db = readFileSync(
      path.join(appFuncDir(outDir, "api"), "src", "lib", "db.js"),
      "utf8",
    );
    expect(db).toContain('"../greeting.js"');
  });

  it("rewrites extensionless relative imports in copied ESM deps (ocel-dist class)", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });
    const funcDir = appFuncDir(outDir, "api");

    const dep = readFileSync(path.join(funcDir, "node_modules", "fake-dep", "index.js"), "utf8");
    expect(dep).toContain('"./helper.js"');
    expect(dep).not.toMatch(/["']\.\/helper["']/);

    const cjs = readFileSync(path.join(funcDir, "node_modules", "cjs-dep", "index.js"), "utf8");
    expect(cjs).toContain('require("./impl")');
  });

  it("emits an entrypoint that imports as an app under raw Node, self-contained", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const funcDir = appFuncDir(outDir, "api");

    const isolated = mkdtempSync(path.join(tmpdir(), "nb-func-"));
    dirs.push(isolated);
    cpSync(funcDir, isolated, { recursive: true });

    const { defaultType } = importEntryInNode(path.join(isolated, "src", "server.js"));
    expect(defaultType).toBe("function");
  });

  it("throws naming the candidates when no entrypoint resolves", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    const emptyDir = mkdtempSync(path.join(tmpdir(), "nb-empty-"));
    dirs.push(emptyDir);
    writeFileSync(path.join(emptyDir, "package.json"), JSON.stringify({ dependencies: { express: "5" } }));

    await expect(
      buildApp({ name: "api", cwd: emptyDir }, { outDir }),
    ).rejects.toThrow(/src\/server\.ts/);
  });

  it("honors an explicit entrypoint override", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    const [summary] = await buildApp(
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
        { name: "worker", cwd: fixtureDir },
      ],
      { outDir },
    );
    expect(summaries.map((s) => s.name)).toEqual(["api", "worker"]);
  });

  it("gives each app its own subtree so a shared route path cannot collide", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApps(
      [
        { name: "storefront", cwd: fixtureDir },
        { name: "admin", cwd: fixtureDir },
      ],
      { outDir },
    );

    for (const app of ["storefront", "admin"]) {
      const config = JSON.parse(readFileSync(path.join(appFuncDir(outDir, app), "config.json"), "utf8"));
      expect(config.app).toBe(app);
    }
  });
});

describe("self-contained .func artifact", () => {
  function buildIsolated(): string {
    const outDir = freshOut();
    dirs.push(outDir);
    return outDir;
  }

  it("places workspace/symlinked packages by identity, not in _external (Defect A)", async () => {
    const outDir = buildIsolated();
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const funcDir = appFuncDir(outDir, "api");
    expect(existsSync(path.join(funcDir, "node_modules", "workspace-pkg", "dist", "index.js"))).toBe(true);
    expect(existsSync(path.join(funcDir, "node_modules", "workspace-pkg", "package.json"))).toBe(true);
    expect(existsSync(path.join(funcDir, "_external"))).toBe(false);

    const isolated = mkdtempSync(path.join(tmpdir(), "nb-func-"));
    dirs.push(isolated);
    cpSync(funcDir, isolated, { recursive: true });
    const { defaultType } = importEntryInNode(path.join(isolated, "src", "server.js"));
    expect(defaultType).toBe("function");
  });

  it("traces deps reached only through typed .ts files (Defect B)", async () => {
    const outDir = buildIsolated();
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const funcDir = appFuncDir(outDir, "api");
    expect(existsSync(path.join(funcDir, "node_modules", "typed-dep", "index.js"))).toBe(true);

    const isolated = mkdtempSync(path.join(tmpdir(), "nb-func-"));
    dirs.push(isolated);
    cpSync(funcDir, isolated, { recursive: true });
    const { defaultType } = importEntryInNode(path.join(isolated, "src", "server.js"));
    expect(defaultType).toBe("function");
  });
});

function nodeApp(dep: string): string {
  const dir = mkdtempSync(path.join(tmpdir(), `nb-${dep}-`));
  dirs.push(dir);
  writeFileSync(path.join(dir, "package.json"), JSON.stringify({ dependencies: { [dep]: "*" } }));
  mkdirSync(path.join(dir, "src"), { recursive: true });
  writeFileSync(path.join(dir, "src", "server.ts"), "export default { fetch: () => new Response() }\n");
  return dir;
}

describe("artifactHash", () => {
  function tree(files: Record<string, string>): string {
    const dir = mkdtempSync(path.join(tmpdir(), "nb-hash-"));
    dirs.push(dir);
    for (const [rel, content] of Object.entries(files)) {
      const abs = path.join(dir, rel);
      mkdirSync(path.dirname(abs), { recursive: true });
      writeFileSync(abs, content);
    }
    return dir;
  }

  it("is 16 lowercase hex chars", async () => {
    expect(await artifactHash(tree({ "a.js": "x" }))).toMatch(/^[0-9a-f]{16}$/);
  });

  it("is identical for identical trees written independently", async () => {
    const files = { "a.js": "one", "nested/b.js": "two", "config.json": "{}" };
    expect(await artifactHash(tree(files))).toBe(await artifactHash(tree(files)));
  });

  it("changes when a file's bytes change", async () => {
    const before = await artifactHash(tree({ "a.js": "one" }));
    expect(await artifactHash(tree({ "a.js": "two" }))).not.toBe(before);
  });

  it("changes when a file is renamed but its bytes are not", async () => {
    const before = await artifactHash(tree({ "a.js": "one" }));
    expect(await artifactHash(tree({ "b.js": "one" }))).not.toBe(before);
  });

  it("changes when a file is added", async () => {
    const before = await artifactHash(tree({ "a.js": "one" }));
    expect(await artifactHash(tree({ "a.js": "one", "b.js": "two" }))).not.toBe(before);
  });
});

describe("serve descriptor", () => {
  function readServe(outDir: string, app: string) {
    return JSON.parse(
      readFileSync(path.join(appOutDir(outDir, app), SERVE_DESCRIPTOR_FILE), "utf8"),
    );
  }

  it("names the framework and an artifact hash at the app artifact root", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    expect(readServe(outDir, "api")).toEqual({
      framework: "express",
      edgeRouting: false,
      needs: {},
      buildId: expect.stringMatching(/^[0-9a-f]{16}$/),
    });
    expect(existsSync(path.join(appFuncDir(outDir, "api"), SERVE_DESCRIPTOR_FILE))).toBe(false);
  });

  it("carries the hash of the function directory", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    expect(readServe(outDir, "api").buildId).toBe(await artifactHash(appFuncDir(outDir, "api")));
  });

  it("repeats the same buildId for an unchanged app", async () => {
    const first = freshOut();
    const second = freshOut();
    dirs.push(first, second);
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir: first });
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir: second });

    expect(readServe(second, "api").buildId).toBe(readServe(first, "api").buildId);
  });
});

describe("a node framework is one server behind one origin", () => {
  for (const framework of ["express", "fastify", "hono"]) {
    it(`${framework} emits exactly one function when tracing, so the edge has one origin to pick`, async () => {
      const outDir = freshOut();
      dirs.push(outDir);

      const summaries = await buildApp({ name: "api", cwd: nodeApp(framework) }, { outDir });

      expect(summaries).toHaveLength(1);
      expect(summaries[0]?.framework).toBe(framework);
      expect(summaries[0]?.strategy).toBe("trace");
      expect(readdirSync(path.join(appOutDir(outDir, "api"), "functions"))).toEqual(["index.func"]);
    });

    it(`${framework} emits exactly one function when bundling, so the edge has one origin to pick`, async () => {
      vi.stubEnv("OCEL_BUILD_PREFER_TRACING", undefined);
      const outDir = freshOut();
      dirs.push(outDir);

      const summaries = await buildApp({ name: "api", cwd: nodeApp(framework) }, { outDir });

      expect(summaries).toHaveLength(1);
      expect(summaries[0]?.framework).toBe(framework);
      expect(summaries[0]?.strategy).toBe("bundle");
      expect(summaries[0]?.artifactPath).toBe(path.join("apps", "api", "functions", "index.func"));
    });
  }
});

describe("writeBuildPlan", () => {
  it("reports every summary, strategy and entrypoint at the output root", async () => {
    vi.stubEnv("OCEL_BUILD_PREFER_TRACING", undefined);
    const outDir = freshOut();
    dirs.push(outDir);
    const cwd = nodeApp("express");

    await writeBuildPlan(outDir, await buildApps([{ name: "api", cwd }], { outDir }));

    expect(JSON.parse(readFileSync(path.join(outDir, BUILD_PLAN_FILE), "utf8"))).toEqual({
      functions: [
        {
          name: "api",
          runtime: "nodejs24.x",
          handler: BUNDLE_HANDLER,
          artifactPath: path.join("apps", "api", "functions", "index.func"),
          framework: "express",
          strategy: "bundle",
          entrypoint: path.join(cwd, "src", "server.ts"),
        },
      ],
    });
  });
});

describe("build strategy", () => {
  it("bundles by default, emitting nothing and reporting the entrypoint", async () => {
    vi.stubEnv("OCEL_BUILD_PREFER_TRACING", undefined);
    const outDir = freshOut();
    dirs.push(outDir);
    const cwd = nodeApp("hono");

    const [summary] = await buildApp({ name: "api", cwd }, { outDir });

    expect(summary).toEqual({
      name: "api",
      runtime: "nodejs24.x",
      handler: BUNDLE_HANDLER,
      artifactPath: path.join("apps", "api", "functions", "index.func"),
      framework: "hono",
      strategy: "bundle",
      entrypoint: path.join(cwd, "src", "server.ts"),
    });
    expect(existsSync(appOutDir(outDir, "api"))).toBe(false);
  });

  it("traces when OCEL_BUILD_PREFER_TRACING is 1", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    const [summary] = await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    expect(summary.strategy).toBe("trace");
    expect(existsSync(path.join(appFuncDir(outDir, "api"), "config.json"))).toBe(true);
  });

  it("bundles for any value other than 1", async () => {
    vi.stubEnv("OCEL_BUILD_PREFER_TRACING", "true");
    const outDir = freshOut();
    dirs.push(outDir);
    const [summary] = await buildApp({ name: "api", cwd: nodeApp("express") }, { outDir });
    expect(summary.strategy).toBe("bundle");
  });

  it("still fails on an unresolvable entrypoint when bundling", async () => {
    vi.stubEnv("OCEL_BUILD_PREFER_TRACING", undefined);
    const outDir = freshOut();
    dirs.push(outDir);
    const dir = mkdtempSync(path.join(tmpdir(), "nb-nobundle-"));
    dirs.push(dir);
    writeFileSync(path.join(dir, "package.json"), JSON.stringify({ dependencies: { hono: "4" } }));

    await expect(buildApp({ name: "api", cwd: dir }, { outDir })).rejects.toThrow(/no entrypoint found/);
  });
});

describe("framework resolution", () => {
  it("throws when a configured app's framework can't be detected", async () => {
    const dir = mkdtempSync(path.join(tmpdir(), "nb-nofw-"));
    dirs.push(dir);
    writeFileSync(path.join(dir, "package.json"), JSON.stringify({ dependencies: { lodash: "4" } }));
    await expect(buildApp({ name: "x", cwd: dir }, { outDir: dir })).rejects.toThrow(/could not detect a framework/);
  });
});

describe("detectApp", () => {
  it("synthesizes a single app named from the dir with the detected framework", () => {
    const dir = mkdtempSync(path.join(tmpdir(), "nb-detect-"));
    dirs.push(dir);
    writeFileSync(path.join(dir, "package.json"), JSON.stringify({ dependencies: { express: "5" } }));
    expect(detectApp(dir)).toEqual({ name: sanitizeName(path.basename(dir)), cwd: dir, framework: "express" });
  });
  it("returns undefined when no framework is detected", () => {
    const dir = mkdtempSync(path.join(tmpdir(), "nb-nodetect-"));
    dirs.push(dir);
    writeFileSync(path.join(dir, "package.json"), JSON.stringify({}));
    expect(detectApp(dir)).toBeUndefined();
  });
});

describe("placeFile", () => {
  const root = mkdtempSync(path.join(tmpdir(), "nb-place-"));
  afterAll(() => rmSync(root, { recursive: true, force: true }));

  function pkg(dir: string, name: string) {
    mkdirSync(path.join(root, dir), { recursive: true });
    writeFileSync(path.join(root, dir, "package.json"), JSON.stringify({ name }));
  }

  const cwd = path.join(root, "app");

  beforeAll(() => {
    mkdirSync(cwd, { recursive: true });
    pkg("packages/ocel", "ocel"); // workspace pkg: real files outside node_modules
    pkg("node_modules/.pnpm/express@5/node_modules/express", "express");
    pkg("node_modules/.pnpm/connect@1/node_modules/@connectrpc/connect", "@connectrpc/connect");
  });

  const at = (p: string) => path.join(root, p);

  it("maps a workspace package (no node_modules segment) by identity", () => {
    expect(placeFile(at("packages/ocel/dist/blob/express.js"), cwd).dest).toBe(
      path.join("node_modules", "ocel", "dist", "blob", "express.js"),
    );
  });

  it("maps a pnpm store path to node_modules/<name>", () => {
    const abs = at("node_modules/.pnpm/express@5/node_modules/express/lib/router.js");
    expect(placeFile(abs, cwd).dest).toBe(path.join("node_modules", "express", "lib", "router.js"));
  });

  it("maps a scoped package name", () => {
    const abs = at("node_modules/.pnpm/connect@1/node_modules/@connectrpc/connect/dist/i.js");
    expect(placeFile(abs, cwd).dest).toBe(
      path.join("node_modules", "@connectrpc", "connect", "dist", "i.js"),
    );
  });

  it("keeps a user file under cwd at the artifact root", () => {
    mkdirSync(path.join(cwd, "src"), { recursive: true });
    writeFileSync(path.join(cwd, "src", "server.ts"), "");
    expect(placeFile(path.join(cwd, "src", "server.ts"), cwd).dest).toBe(path.join("src", "server.ts"));
  });
});

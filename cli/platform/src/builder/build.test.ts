import { execFileSync } from "node:child_process";
import {
  cpSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { buildApp, buildApps, detectApp, placeFile } from "./build.js";
import { sanitizeName } from "./detect.js";
import { appOutDir } from "./layout.js";

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

(() => {
  const link = path.join(fixtureDir, "node_modules", "workspace-pkg");
  try {
    lstatSync(link);
  } catch {
    symlinkSync(path.join("..", "..", "workspace-pkg"), link, "dir");
  }
})();

const outRoot = path.resolve(here, "../../.ocel");

function freshOut(): string {
  mkdirSync(outRoot, { recursive: true });
  return mkdtempSync(path.join(outRoot, "test-"));
}

const dirs: string[] = [];
afterAll(() => {
  for (const d of dirs) rmSync(d, { recursive: true, force: true });
});

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

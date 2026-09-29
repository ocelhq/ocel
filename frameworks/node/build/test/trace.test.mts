import { execFileSync } from "node:child_process";
import {
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { DuplicatePackageError, placeFile, placeTrace, traceFunction } from "../src/trace.mjs";

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
const fixtureDir = path.join(here, "fixtures", "express-app");
const entrypoint = path.join(fixtureDir, "src", "server.ts");

const dirs: string[] = [];
afterAll(() => {
  for (const d of dirs) rmSync(d, { recursive: true, force: true });
});

function scratch(prefix: string): string {
  const dir = mkdtempSync(path.join(tmpdir(), prefix));
  dirs.push(dir);
  return dir;
}

async function traceFixture(): Promise<string> {
  const functionDir = path.join(scratch("nb-out-"), "index.func");
  await traceFunction({ cwd: fixtureDir, entrypoint, functionDir });
  return functionDir;
}

function importsAsAnApp(functionDir: string): string {
  const isolated = scratch("nb-func-");
  cpSync(functionDir, isolated, { recursive: true });
  return importEntryInNode(path.join(isolated, "src", "server.js")).defaultType;
}

describe("traceFunction", () => {
  let functionDir: string;
  beforeAll(async () => {
    functionDir = await traceFixture();
  });

  it("emits the traced sources at their own paths, not one bundle and no config", () => {
    expect(existsSync(path.join(functionDir, "index.mjs"))).toBe(false);
    expect(existsSync(path.join(functionDir, "config.json"))).toBe(false);
    expect(existsSync(path.join(functionDir, "src", "server.js"))).toBe(true);
    expect(existsSync(path.join(functionDir, "src", "greeting.js"))).toBe(true);
  });

  it("preserves the module tree instead of emitting a single bundle", () => {
    const server = readFileSync(path.join(functionDir, "src", "server.js"), "utf8");
    expect(server).toContain('from "express"');
    expect(server).toContain("./greeting.js");
    expect(existsSync(path.join(functionDir, "node_modules", "express"))).toBe(true);
  });

  it("strips types but preserves modern syntax verbatim (no downleveling)", () => {
    const server = readFileSync(path.join(functionDir, "src", "server.js"), "utf8");
    expect(server).toContain("req.params?.name ?? ");
    expect(server).not.toContain("_optionalChain");
    expect(server).not.toContain("_nullishCoalesce");
    expect(server).not.toMatch(/:\s*(string|number|Request)\b/);
  });

  it("rewrites extensionless relative specifiers, leaving bare/extensioned alone", () => {
    const server = readFileSync(path.join(functionDir, "src", "server.js"), "utf8");
    expect(server).toContain('"./lib/db.js"');
    expect(server).not.toMatch(/["']\.\/lib\/db["']/);
    expect(server).toContain('"./config/index.js"');
    expect(server).not.toMatch(/["']\.\/config["']/);
    expect(server).toContain('from "express"');
    expect(server).toContain('"./greeting.js"');

    const db = readFileSync(path.join(functionDir, "src", "lib", "db.js"), "utf8");
    expect(db).toContain('"../greeting.js"');
  });

  it("rewrites extensionless relative imports in copied ESM deps (ocel-dist class)", () => {
    const dep = readFileSync(
      path.join(functionDir, "node_modules", "fake-dep", "index.js"),
      "utf8",
    );
    expect(dep).toContain('"./helper.js"');
    expect(dep).not.toMatch(/["']\.\/helper["']/);

    const cjs = readFileSync(path.join(functionDir, "node_modules", "cjs-dep", "index.js"), "utf8");
    expect(cjs).toContain('require("./impl")');
  });

  it("emits an entrypoint that imports as an app under raw Node, self-contained", () => {
    expect(importsAsAnApp(functionDir)).toBe("function");
  });

  it("places workspace/symlinked packages by identity, not in _external (Defect A)", () => {
    expect(
      existsSync(path.join(functionDir, "node_modules", "workspace-pkg", "dist", "index.js")),
    ).toBe(true);
    expect(
      existsSync(path.join(functionDir, "node_modules", "workspace-pkg", "package.json")),
    ).toBe(true);
    expect(existsSync(path.join(functionDir, "_external"))).toBe(false);
  });

  it("traces deps reached only through typed .ts files (Defect B)", () => {
    expect(existsSync(path.join(functionDir, "node_modules", "typed-dep", "index.js"))).toBe(true);
  });

  it("replaces whatever an earlier build left in the function directory", async () => {
    const stale = path.join(scratch("nb-out-"), "index.func");
    mkdirSync(stale, { recursive: true });
    writeFileSync(path.join(stale, "stale.js"), "");

    await traceFunction({ cwd: fixtureDir, entrypoint, functionDir: stale });

    expect(existsSync(path.join(stale, "stale.js"))).toBe(false);
    expect(existsSync(path.join(stale, "src", "server.js"))).toBe(true);
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
    pkg("packages/ocel", "ocel");
    pkg("node_modules/.pnpm/express@5/node_modules/express", "express");
    pkg("node_modules/.pnpm/connect@1/node_modules/@connectrpc/connect", "@connectrpc/connect");
  });

  const at = (p: string) => path.join(root, p);

  it("maps a workspace package (no node_modules segment) by identity", () => {
    expect(placeFile(at("packages/ocel/dist/bucket/express.js"), cwd).dest).toBe(
      path.join("node_modules", "ocel", "dist", "bucket", "express.js"),
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
    expect(placeFile(path.join(cwd, "src", "server.ts"), cwd).dest).toBe(
      path.join("src", "server.ts"),
    );
  });
});

describe("placeTrace", () => {
  const root = mkdtempSync(path.join(tmpdir(), "nb-trace-"));
  afterAll(() => rmSync(root, { recursive: true, force: true }));
  const at = (p: string) => path.join(root, p);
  const cwd = at("app");

  function packageAt(dir: string, name: string, version: string) {
    mkdirSync(at(dir), { recursive: true });
    writeFileSync(at(path.join(dir, "package.json")), JSON.stringify({ name, version }));
    writeFileSync(at(path.join(dir, "index.js")), "");
  }

  beforeAll(() => {
    mkdirSync(path.join(cwd, "src"), { recursive: true });
    writeFileSync(path.join(cwd, "src", "server.js"), "");
    packageAt("node_modules/.pnpm/dep@1/node_modules/dep", "dep", "1.0.0");
    packageAt("node_modules/.pnpm/dep@2/node_modules/dep", "dep", "2.0.0");
    packageAt("node_modules/.pnpm/lib@1/node_modules/lib", "lib", "1.0.0");
  });

  const server = path.join(cwd, "src", "server.js");
  const depOne = at("node_modules/.pnpm/dep@1/node_modules/dep/index.js");
  const depTwo = at("node_modules/.pnpm/dep@2/node_modules/dep/index.js");
  const lib = at("node_modules/.pnpm/lib@1/node_modules/lib/index.js");

  it("nests a second version of a package under the package that imports it", () => {
    const parents: Record<string, string[]> = {
      [depOne]: [server],
      [lib]: [server],
      [depTwo]: [lib],
    };
    const placed = placeTrace([server, depOne, lib, depTwo], (file) => parents[file] ?? [], cwd);

    expect(placed.get(depOne)).toEqual([path.join("node_modules", "dep", "index.js")]);
    expect(placed.get(depTwo)).toEqual([
      path.join("node_modules", "lib", "node_modules", "dep", "index.js"),
    ]);
    expect(placed.get(at("node_modules/.pnpm/dep@2/node_modules/dep/package.json"))).toEqual([
      path.join("node_modules", "lib", "node_modules", "dep", "package.json"),
    ]);
  });

  it("refuses an app whose own code imports two copies of one package", () => {
    const parents: Record<string, string[]> = { [depOne]: [server], [depTwo]: [server] };
    expect(() => placeTrace([server, depOne, depTwo], (file) => parents[file] ?? [], cwd)).toThrow(
      DuplicatePackageError,
    );
  });

  it("keeps two same-named files outside any package apart", () => {
    mkdirSync(at("shared/a"), { recursive: true });
    mkdirSync(at("shared/b"), { recursive: true });
    writeFileSync(at("shared/a/config.json"), "{}");
    writeFileSync(at("shared/b/config.json"), "{}");

    const placed = placeTrace(
      [at("shared/a/config.json"), at("shared/b/config.json")],
      () => [],
      cwd,
    );

    const [first] = placed.get(at("shared/a/config.json")) ?? [];
    const [second] = placed.get(at("shared/b/config.json")) ?? [];
    expect(first).toMatch(/^_external/);
    expect(first).not.toBe(second);
  });
});

describe("traceFunction with two versions of one dependency", () => {
  it("serves each importer the version it resolved", async () => {
    const project = scratch("nb-versions-");
    const write = (file: string, content: string) => {
      mkdirSync(path.dirname(path.join(project, file)), { recursive: true });
      writeFileSync(path.join(project, file), content);
    };
    write("package.json", JSON.stringify({ name: "app", type: "module" }));
    write(
      "src/server.mjs",
      'import dep from "dep";\nimport lib from "lib";\nexport default () => dep + ":" + lib;\n',
    );
    write(
      "node_modules/dep/package.json",
      JSON.stringify({ name: "dep", version: "1.0.0", main: "index.js" }),
    );
    write("node_modules/dep/index.js", 'module.exports = "dep1";\n');
    write(
      "node_modules/lib/package.json",
      JSON.stringify({ name: "lib", version: "1.0.0", main: "index.js" }),
    );
    write("node_modules/lib/index.js", 'module.exports = require("dep");\n');
    write(
      "node_modules/lib/node_modules/dep/package.json",
      JSON.stringify({ name: "dep", version: "2.0.0", main: "index.js" }),
    );
    write("node_modules/lib/node_modules/dep/index.js", 'module.exports = "dep2";\n');

    const functionDir = path.join(scratch("nb-out-"), "index.func");
    await traceFunction({
      cwd: project,
      entrypoint: path.join(project, "src", "server.mjs"),
      functionDir,
    });

    const script =
      `const mod = await import(${JSON.stringify(pathToFileURL(path.join(functionDir, "src", "server.mjs")).href)});\n` +
      "process.stdout.write(mod.default());";
    expect(execFileSync("node", ["--input-type=module", "-e", script], { encoding: "utf8" })).toBe(
      "dep1:dep2",
    );
  });
});

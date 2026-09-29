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
import { placeFile, traceFunction } from "../src/trace.mjs";

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
  const funcDir = path.join(scratch("nb-out-"), "index.func");
  await traceFunction({ cwd: fixtureDir, entrypoint, funcDir });
  return funcDir;
}

function importsAsAnApp(funcDir: string): string {
  const isolated = scratch("nb-func-");
  cpSync(funcDir, isolated, { recursive: true });
  return importEntryInNode(path.join(isolated, "src", "server.js")).defaultType;
}

describe("traceFunction", () => {
  let funcDir: string;
  beforeAll(async () => {
    funcDir = await traceFixture();
  });

  it("emits the traced sources at their own paths, not one bundle and no config", () => {
    expect(existsSync(path.join(funcDir, "index.mjs"))).toBe(false);
    expect(existsSync(path.join(funcDir, "config.json"))).toBe(false);
    expect(existsSync(path.join(funcDir, "src", "server.js"))).toBe(true);
    expect(existsSync(path.join(funcDir, "src", "greeting.js"))).toBe(true);
  });

  it("preserves the module tree instead of emitting a single bundle", () => {
    const server = readFileSync(path.join(funcDir, "src", "server.js"), "utf8");
    expect(server).toContain('from "express"');
    expect(server).toContain("./greeting.js");
    expect(existsSync(path.join(funcDir, "node_modules", "express"))).toBe(true);
  });

  it("strips types but preserves modern syntax verbatim (no downleveling)", () => {
    const server = readFileSync(path.join(funcDir, "src", "server.js"), "utf8");
    expect(server).toContain("req.params?.name ?? ");
    expect(server).not.toContain("_optionalChain");
    expect(server).not.toContain("_nullishCoalesce");
    expect(server).not.toMatch(/:\s*(string|number|Request)\b/);
  });

  it("rewrites extensionless relative specifiers, leaving bare/extensioned alone", () => {
    const server = readFileSync(path.join(funcDir, "src", "server.js"), "utf8");
    expect(server).toContain('"./lib/db.js"');
    expect(server).not.toMatch(/["']\.\/lib\/db["']/);
    expect(server).toContain('"./config/index.js"');
    expect(server).not.toMatch(/["']\.\/config["']/);
    expect(server).toContain('from "express"');
    expect(server).toContain('"./greeting.js"');

    const db = readFileSync(path.join(funcDir, "src", "lib", "db.js"), "utf8");
    expect(db).toContain('"../greeting.js"');
  });

  it("rewrites extensionless relative imports in copied ESM deps (ocel-dist class)", () => {
    const dep = readFileSync(path.join(funcDir, "node_modules", "fake-dep", "index.js"), "utf8");
    expect(dep).toContain('"./helper.js"');
    expect(dep).not.toMatch(/["']\.\/helper["']/);

    const cjs = readFileSync(path.join(funcDir, "node_modules", "cjs-dep", "index.js"), "utf8");
    expect(cjs).toContain('require("./impl")');
  });

  it("emits an entrypoint that imports as an app under raw Node, self-contained", () => {
    expect(importsAsAnApp(funcDir)).toBe("function");
  });

  it("places workspace/symlinked packages by identity, not in _external (Defect A)", () => {
    expect(
      existsSync(path.join(funcDir, "node_modules", "workspace-pkg", "dist", "index.js")),
    ).toBe(true);
    expect(existsSync(path.join(funcDir, "node_modules", "workspace-pkg", "package.json"))).toBe(
      true,
    );
    expect(existsSync(path.join(funcDir, "_external"))).toBe(false);
  });

  it("traces deps reached only through typed .ts files (Defect B)", () => {
    expect(existsSync(path.join(funcDir, "node_modules", "typed-dep", "index.js"))).toBe(true);
  });

  it("replaces whatever an earlier build left in the function directory", async () => {
    const stale = path.join(scratch("nb-out-"), "index.func");
    mkdirSync(stale, { recursive: true });
    writeFileSync(path.join(stale, "stale.js"), "");

    await traceFunction({ cwd: fixtureDir, entrypoint, funcDir: stale });

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

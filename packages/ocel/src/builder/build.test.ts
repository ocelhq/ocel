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

// Import a built entrypoint in a REAL Node ESM process and report the type of
// its default export. This is what the lambdanode entrypoint does (OCEL_HANDLER points
// at this file); a clean import proves the whole traced module tree resolves
// under raw Node — vitest's own `await import` goes through Vite's bundler-style
// resolver, which resolves extensionless imports and would mask the raw-Node
// ERR_MODULE_NOT_FOUND we must guard. A missing transitive dep throws here.
function importEntryInNode(entryMjs: string): { defaultType: string } {
  const script =
    `const mod = await import(${JSON.stringify(pathToFileURL(entryMjs).href)});\n` +
    // Sentinels isolate the result from the app's own stdout.
    `process.stdout.write("__RES__" + JSON.stringify({ defaultType: typeof mod.default }) + "__END__");`;
  const out = execFileSync("node", ["--input-type=module", "-e", script], { encoding: "utf8" });
  const match = out.match(/__RES__([\s\S]*)__END__/);
  if (!match) throw new Error(`no import result in output:\n${out}`);
  return JSON.parse(match[1] as string);
}

const here = path.dirname(fileURLToPath(import.meta.url));
const fixtureDir = path.resolve(here, "../../test/fixtures/express-app");

// Simulate a pnpm workspace link: `workspace-pkg`'s real files live outside any
// node_modules and are symlinked into the app's node_modules. Created here (not
// committed) so nft follows the link to the real, out-of-node_modules location.
(() => {
  const link = path.join(fixtureDir, "node_modules", "workspace-pkg");
  try {
    lstatSync(link);
  } catch {
    symlinkSync(path.join("..", "..", "workspace-pkg"), link, "dir");
  }
})();

// Keep build output under the package so Node's upward node_modules lookup can
// resolve `express` at runtime; `.ocel` is gitignored repo-wide.
const outRoot = path.resolve(here, "../../.ocel");

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

    const [summary] = await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const funcDir = path.join(outDir, "functions", "api.func");
    // No generated shim: the runtime imports the user's entrypoint directly.
    expect(existsSync(path.join(funcDir, "index.mjs"))).toBe(false);
    expect(existsSync(path.join(funcDir, "config.json"))).toBe(true);
    expect(existsSync(path.join(funcDir, "src", "server.js"))).toBe(true);
    // JS helper is copied verbatim, TS entrypoint is transpiled next to it.
    expect(existsSync(path.join(funcDir, "src", "greeting.js"))).toBe(true);

    const config = JSON.parse(readFileSync(path.join(funcDir, "config.json"), "utf8"));
    expect(config).toEqual({
      runtime: "nodejs24.x",
      // handler is the entrypoint path within the .func; OCEL_HANDLER resolves
      // it as /var/task/<handler>.
      handler: "src/server.js",
      framework: "express",
    });

    expect(summary.name).toBe("api");
    expect(summary.runtime).toBe("nodejs24.x");
    expect(summary.handler).toBe("src/server.js");
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

  it("strips types but preserves modern syntax verbatim (no downleveling)", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const server = readFileSync(
      path.join(outDir, "functions", "api.func", "src", "server.js"),
      "utf8",
    );
    // Optional chaining and nullish coalescing survive to the emitted output;
    // nodejs24.x runs them natively, so downleveling only obscures user code.
    expect(server).toContain("req.params?.name ?? ");
    expect(server).not.toContain("_optionalChain");
    expect(server).not.toContain("_nullishCoalesce");
    // Types are still stripped.
    expect(server).not.toMatch(/:\s*(string|number|Request)\b/);
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

  it("emits an entrypoint that imports as an app under raw Node, self-contained", async () => {
    const outDir = freshOut();
    dirs.push(outDir);
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const funcDir = path.join(outDir, "functions", "api.func");

    // Copy the artifact outside the repo (no ancestor node_modules) and import
    // the entrypoint there: a clean import proves every runtime dep travels
    // inside the .func and the whole tree resolves under raw Node ESM.
    const isolated = mkdtempSync(path.join(tmpdir(), "nb-func-"));
    dirs.push(isolated);
    cpSync(funcDir, isolated, { recursive: true });

    const { defaultType } = importEntryInNode(path.join(isolated, "src", "server.js"));
    // The express app is the default export the lambdanode entrypoint serves.
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
});

// A built `.func` is copied into a real Lambda-style sandbox and run with the
// user's node — no dev tools, no ancestor node_modules. Every runtime dep must
// therefore travel inside the artifact and resolve under raw Node ESM.
describe("self-contained .func artifact", () => {
  function buildIsolated(): string {
    const outDir = freshOut();
    dirs.push(outDir);
    return outDir;
  }

  it("places workspace/symlinked packages by identity, not in _external (Defect A)", async () => {
    const outDir = buildIsolated();
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const funcDir = path.join(outDir, "functions", "api.func");
    // The workspace pkg's real files live outside node_modules; they must be
    // reconstructed under node_modules/<name>, WITH its package.json, not dumped
    // in _external.
    expect(existsSync(path.join(funcDir, "node_modules", "workspace-pkg", "dist", "index.js"))).toBe(true);
    expect(existsSync(path.join(funcDir, "node_modules", "workspace-pkg", "package.json"))).toBe(true);
    expect(existsSync(path.join(funcDir, "_external"))).toBe(false);

    // The entrypoint imports `workspace-pkg` at the top level; copy the artifact
    // outside the repo and import it there, proving it resolves under raw Node.
    const isolated = mkdtempSync(path.join(tmpdir(), "nb-func-"));
    dirs.push(isolated);
    cpSync(funcDir, isolated, { recursive: true });
    const { defaultType } = importEntryInNode(path.join(isolated, "src", "server.js"));
    expect(defaultType).toBe("function");
  });

  it("traces deps reached only through typed .ts files (Defect B)", async () => {
    const outDir = buildIsolated();
    await buildApp({ name: "api", cwd: fixtureDir }, { outDir });

    const funcDir = path.join(outDir, "functions", "api.func");
    // typed-dep is imported ONLY from the typed src/lib/db.ts. nft under-traced
    // it before the readFile transpile hook; it must now be in the artifact.
    expect(existsSync(path.join(funcDir, "node_modules", "typed-dep", "index.js"))).toBe(true);

    // typed-dep is reached only through src/lib/db.ts, imported by the
    // entrypoint; a clean isolated import proves it traveled into the artifact.
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

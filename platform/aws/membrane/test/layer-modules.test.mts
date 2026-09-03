import { execFile } from "node:child_process";
import { mkdtemp, readdir, readFile, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join, relative, resolve } from "node:path";
import { promisify } from "node:util";
import { afterAll, beforeAll, expect, test } from "vitest";

const execFileAsync = promisify(execFile);
const pkgDir = resolve(import.meta.dirname, "..");
const tsc = createRequire(import.meta.url).resolve("typescript/bin/tsc");

const specifierPattern =
  /\b(?:import|export)\b[^"'`;]*?\bfrom\s*["']([^"']+)["']|\bimport\s*["']([^"']+)["']/g;

let dist: string;

beforeAll(async () => {
  dist = await mkdtemp(join(tmpdir(), "ocel-layer-"));
  await execFileAsync(process.execPath, [tsc, "--outDir", dist], { cwd: pkgDir });
  await execFileAsync(process.execPath, ["scripts/bundle-modules.mjs", relative(pkgDir, dist)], {
    cwd: pkgDir,
  });
}, 120_000);

afterAll(async () => {
  await rm(dist, { recursive: true, force: true });
});

async function looseModules(): Promise<string[]> {
  const entries = await readdir(dist, { recursive: true, withFileTypes: true });
  return entries
    .filter((entry) => entry.isFile() && entry.name.endsWith(".mjs"))
    .map((entry) => join(entry.parentPath, entry.name))
    .sort();
}

async function specifiersOf(file: string): Promise<string[]> {
  const source = await readFile(file, "utf8");
  return [...source.matchAll(specifierPattern)].map((match) => match[1] ?? match[2]!);
}

test("no loose layer module imports a package the layer does not carry", async () => {
  const offenders: Record<string, string[]> = {};
  for (const file of await looseModules()) {
    const bare = (await specifiersOf(file)).filter(
      (specifier) => !specifier.startsWith(".") && !specifier.startsWith("node:"),
    );
    if (bare.length > 0) offenders[relative(dist, file)] = bare;
  }
  expect(offenders).toEqual({});
});

test("every relative import in the layer lands on a module the layer ships", async () => {
  const shipped = new Set(await looseModules());
  const missing: string[] = [];
  for (const file of shipped) {
    for (const specifier of await specifiersOf(file)) {
      if (!specifier.startsWith(".")) continue;
      const target = resolve(file, "..", specifier);
      if (!shipped.has(target)) missing.push(`${relative(dist, file)} -> ${specifier}`);
    }
  }
  expect(missing).toEqual([]);
  expect(shipped.has(join(dist, "next", "entrypoint.mjs"))).toBe(true);
  expect(shipped.has(join(dist, "node", "entrypoint.mjs"))).toBe(true);
});

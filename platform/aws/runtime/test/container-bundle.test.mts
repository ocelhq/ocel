import { execFile } from "node:child_process";
import { mkdtemp, readdir, readFile, rm } from "node:fs/promises";
import { isBuiltin } from "node:module";
import { tmpdir } from "node:os";
import { join, relative, resolve } from "node:path";
import { promisify } from "node:util";
import { init, parse } from "es-module-lexer";
import { afterAll, beforeAll, expect, test } from "vitest";

const execFileAsync = promisify(execFile);
const pkgDir = resolve(import.meta.dirname, "..");

let dist: string;

beforeAll(async () => {
  dist = await mkdtemp(join(tmpdir(), "ocel-aws-container-"));
  await execFileAsync("bun", ["scripts/bundle-modules.mjs", relative(pkgDir, dist)], {
    cwd: pkgDir,
  });
}, 120_000);

afterAll(async () => {
  await rm(dist, { recursive: true, force: true });
});

test("the container directory holds the Next runtime a container built from its function boots, importing nothing but Node's own modules", async () => {
  const entries = await readdir(join(dist, "container"), { recursive: true, withFileTypes: true });
  const files = entries
    .filter((entry) => entry.isFile())
    .map((entry) => relative(join(dist, "container"), join(entry.parentPath, entry.name)));
  expect(files).toEqual(["next/entrypoint.mjs"]);

  await init;
  const [imports] = parse(await readFile(join(dist, "container", "next/entrypoint.mjs"), "utf8"));
  const bare = imports
    .flatMap((found) => (found.n === undefined ? [] : [found.n]))
    .filter((specifier) => !isBuiltin(specifier));
  expect(bare).toEqual([]);
});

test("the layer ships neither the container's Next runtime nor the hosts it installs", async () => {
  const entries = await readdir(join(dist, "next"));
  expect(entries).not.toContain("container-next-host.mjs");
  expect(entries).not.toContain("function-container-host.mjs");
  expect(entries).not.toContain("container-entrypoint.mjs");
});

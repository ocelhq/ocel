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
  dist = await mkdtemp(join(tmpdir(), "ocel-vps-runtime-"));
  await execFileAsync("bun", ["scripts/bundle-next.mjs", relative(pkgDir, dist)], { cwd: pkgDir });
}, 300_000);

afterAll(async () => {
  await rm(dist, { recursive: true, force: true });
});

test("the Next runtime a box's container boots is one file importing nothing but Node's own modules and sharp", async () => {
  await init;
  const [imports] = parse(await readFile(join(dist, "next", "entrypoint.mjs"), "utf8"));
  const bare = imports
    .flatMap((found) => (found.n === undefined ? [] : [found.n]))
    .filter((specifier) => !isBuiltin(specifier));
  expect([...new Set(bare)]).toEqual(["sharp"]);
});

test("sharp is staged for both architectures a box runs, with libvips's notices", async () => {
  for (const cpu of ["x64", "arm64"]) {
    const staged = await readdir(join(dist, "sharp", cpu));
    expect(staged.sort(), cpu).toEqual([
      "GPL-3.0.txt",
      "LGPL-3.0.txt",
      "THIRD_PARTY_NOTICES",
      "node_modules",
    ]);
    const img = await readdir(join(dist, "sharp", cpu, "node_modules", "@img"));
    expect(img, cpu).toEqual(
      expect.arrayContaining([`sharp-linux-${cpu}`, `sharp-libvips-linux-${cpu}`]),
    );
  }
});

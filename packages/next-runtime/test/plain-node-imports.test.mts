import { execFile } from "node:child_process";
import { readdir, readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { expect, test } from "vitest";

const run = promisify(execFile);
const packageDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const srcDir = join(packageDir, "src");

// Every specifier this package imports from another workspace package, as its
// own source spells them.
async function workspaceSpecifiers(): Promise<string[]> {
  const files = (await readdir(srcDir)).filter((f) => f.endsWith(".mts"));
  const found = new Set<string>();
  for (const file of files) {
    const source = await readFile(join(srcDir, file), "utf8");
    for (const [, specifier] of source.matchAll(
      /^import[^"']*["'](@ocel\/[^"']+)["']/gm,
    )) {
      found.add(specifier!);
    }
  }
  return [...found].sort();
}

// This package's build is `tsc`, so its dist is a tree of loose modules that
// plain Node executes with a bare workspace specifier left in it — and Node
// resolves such a specifier to raw TypeScript, since no workspace package here
// ships compiled output. It gets away with that only while the module on the
// other end is one Node can load: no relative imports, whose `.mjs` specifiers
// Node does not rewrite, and no syntax it cannot erase (enum, namespace,
// parameter properties).
//
// Nothing else in the repo would notice that breaking. Every other consumer of
// @ocel/* is bundled — esbuild for the Lambda and for the copy of this adapter
// that ships inside the CLI, wrangler for the workers — and a bundler resolves
// the whole graph itself, so typecheck, all six suites here and every build stay
// green while `next build` fails at the first import. So the check is to make
// Node do exactly what the built adapter does: resolve from this package, over
// the real node_modules link, and load it.
test("every workspace specifier the source imports loads under plain Node", async () => {
  const specifiers = await workspaceSpecifiers();
  expect(specifiers.length).toBeGreaterThan(0);

  for (const specifier of specifiers) {
    await expect(
      run(process.execPath, ["--input-type=module", "-e", `import ${JSON.stringify(specifier)}`], {
        cwd: packageDir,
      }),
      `${specifier} is imported by this package's source but plain Node cannot load it, so the built adapter would fail at \`next build\``,
    ).resolves.toBeDefined();
  }
});

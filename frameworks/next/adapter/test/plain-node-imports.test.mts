import { execFile } from "node:child_process";
import { readdir, readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { expect, test } from "vitest";

const run = promisify(execFile);
const packageDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const srcDir = join(packageDir, "src");

async function workspaceDependencies(): Promise<Set<string>> {
  const manifest = JSON.parse(
    await readFile(join(packageDir, "package.json"), "utf8"),
  ) as { dependencies?: Record<string, string> };
  return new Set(
    Object.entries(manifest.dependencies ?? {})
      .filter(([, range]) => range.startsWith("workspace:"))
      .map(([name]) => name),
  );
}

function packageOf(specifier: string): string {
  const segments = specifier.split("/");
  return specifier.startsWith("@") ? segments.slice(0, 2).join("/") : segments[0]!;
}

async function workspaceSpecifiers(): Promise<string[]> {
  const workspaceDeps = await workspaceDependencies();
  const files = (await readdir(srcDir)).filter((f) => f.endsWith(".mts"));
  const found = new Set<string>();
  for (const file of files) {
    const source = await readFile(join(srcDir, file), "utf8");
    for (const [, specifier] of source.matchAll(
      /^import[^"']*["']([^"'.][^"']*)["']/gm,
    )) {
      if (workspaceDeps.has(packageOf(specifier!))) found.add(specifier!);
    }
  }
  return [...found].sort();
}

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

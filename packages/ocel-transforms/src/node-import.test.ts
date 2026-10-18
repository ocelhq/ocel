import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { beforeAll, expect, it } from "vitest";

const packageDir = fileURLToPath(new URL("..", import.meta.url));
const { exports } = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8"));
const tsc = createRequire(import.meta.url).resolve("typescript/bin/tsc");

beforeAll(() => {
  execFileSync(process.execPath, [tsc, "-p", "tsconfig.json"], { cwd: packageDir });
}, 120_000);

it.each(
  Object.keys(exports)
    .filter((path) => path !== "./package.json")
    .map((path) => path.replace(/^\./, "@ocel/transforms")),
)("imports %s from the built package with plain node", (specifier) => {
  const imported = execFileSync(
    process.execPath,
    [
      "--input-type=module",
      "-e",
      `const m = await import(${JSON.stringify(specifier)}); console.log(Object.keys(m).length);`,
    ],
    { cwd: packageDir, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] },
  );

  expect(Number(imported.trim())).toBeGreaterThan(0);
});

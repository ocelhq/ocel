import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { beforeAll, expect, it } from "vitest";

const packageDir = fileURLToPath(new URL("..", import.meta.url));
const manifest = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8"));
const tsc = createRequire(import.meta.url).resolve("typescript/bin/tsc");
const subpaths = Object.keys(manifest.exports).filter((path) => path !== "./package.json");

beforeAll(() => {
  execFileSync(process.execPath, [tsc, "-p", "tsconfig.json"], { cwd: packageDir });
}, 120_000);

it.each(subpaths.map((path) => path.replace(/^\./, "ocel")))(
  "requires %s from the built package with plain node",
  (specifier) => {
    const required = execFileSync(
      process.execPath,
      ["-e", `console.log(typeof require(${JSON.stringify(specifier)}));`],
      {
        cwd: packageDir,
        encoding: "utf8",
        env: { ...process.env, OCEL_PHASE: "", OCEL_DEV_SERVER: "" },
        stdio: ["ignore", "pipe", "pipe"],
      },
    );

    expect(required.trim()).toBe("object");
  },
);

it("names the files whose import has side effects, so bundlers can drop the rest", () => {
  expect(Array.isArray(manifest.sideEffects)).toBe(true);
});

it.each([
  "task/task.d.ts",
  "task/runs.d.ts",
  "topic/topic.d.ts",
  "topic/dead-letters.d.ts",
  "bucket/route.d.ts",
  "bucket/types.d.ts",
  "bucket/uploader.d.ts",
  "bucket/next.d.ts",
  "bucket/hono.d.ts",
  "bucket/express.d.ts",
  "postgres/index.d.ts",
])("declares %s without a generated or transport type", (file) => {
  const declarations = readFileSync(new URL(`../dist/${file}`, import.meta.url), "utf8");

  expect(declarations).not.toMatch(/gen\/proto|@bufbuild|@connectrpc/);
});

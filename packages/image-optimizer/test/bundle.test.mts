// The bundle has to survive being loaded, and nothing else here proves that.
// Every other test in this package imports src/ directly, under vitest, which
// resolves CJS dependencies the way Node resolves them on disk. Lambda does not:
// it loads one bundled index.mjs, and the interop esbuild writes into that file
// is the thing that can be wrong. It was — a released artifact answered every
// request with "Dynamic require of \"node:https\" is not supported", thrown out
// of the AWS SDK's transport while the module graph was still loading.
//
// So this builds the artifact's bundle with the artifact's flags and imports it
// in a plain Node ESM process, which is the same act Lambda performs and the
// only one that exercises the shim.
import { execFileSync } from "node:child_process";
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { esbuildArgs } from "../scripts/bundle.mjs";

const root = dirname(dirname(fileURLToPath(import.meta.url)));

// Built inside the package so that sharp — external, and the one dependency the
// bundle expects to find on disk — resolves up into the package's own
// node_modules, exactly as it resolves out of node_modules/ in the zip.
const out = join(root, "dist", "bundle-test");

describe("the deployable bundle", () => {
  it("loads under Node's ESM loader", () => {
    rmSync(out, { recursive: true, force: true });
    mkdirSync(out, { recursive: true });

    execFileSync(
      "pnpm",
      ["exec", "esbuild", ...esbuildArgs(join(root, "src", "index.mts"), join(out, "index.mjs"))],
      { cwd: root, stdio: "pipe" },
    );

    // A file rather than `node -e`, because `-e` evaluates as CommonJS and leaves
    // a real `require` in scope. Under it a bundle with no banner at all loads
    // clean, and this test would pass against the exact artifact that fails in
    // production.
    const probe = join(out, "probe.mjs");
    writeFileSync(
      probe,
      'import("./index.mjs").then(() => console.log("loaded"));\n',
    );

    expect(execFileSync(process.execPath, [probe], { encoding: "utf8" }).trim()).toBe(
      "loaded",
    );
  }, 120_000);
});

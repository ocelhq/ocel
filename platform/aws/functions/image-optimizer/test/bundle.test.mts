import { execFileSync } from "node:child_process";
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { esbuildArgs } from "../scripts/bundle.mjs";

const root = dirname(dirname(fileURLToPath(import.meta.url)));

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

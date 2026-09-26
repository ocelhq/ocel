import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { bunArgs } from "../scripts/bundle.mjs";

const root = dirname(dirname(fileURLToPath(import.meta.url)));

const out = join(root, "dist", "bundle-test");

function bundled(): string {
  rmSync(out, { recursive: true, force: true });
  mkdirSync(out, { recursive: true });

  execFileSync(
    "bun",
    ["build", ...bunArgs(join(root, "src", "index.mts"), join(out, "index.mjs"))],
    { cwd: root, stdio: "pipe" },
  );
  return join(out, "index.mjs");
}

describe("the deployable bundle", () => {
  it("loads under Node's ESM loader", () => {
    bundled();

    const probe = join(out, "probe.mjs");
    writeFileSync(probe, 'import("./index.mjs").then(() => console.log("loaded"));\n');

    expect(execFileSync(process.execPath, [probe], { encoding: "utf8" }).trim()).toBe("loaded");
  }, 120_000);

  it("contains no path of the checkout it was built in", () => {
    const checkout = join(root, "..", "..", "..", "..");

    expect(readFileSync(bundled(), "utf8")).not.toContain(checkout);
  }, 120_000);
});

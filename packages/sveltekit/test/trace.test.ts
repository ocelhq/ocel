import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { bundleFunction } from "../src/trace.js";

describe("bundleFunction", () => {
  it("runs a dependency reached through a node_modules the app links to elsewhere", async () => {
    const root = mkdtempSync(join(tmpdir(), "ocel-sveltekit-trace-"));
    mkdirSync(join(root, "store/pkg"), { recursive: true });
    writeFileSync(join(root, "store/pkg/package.json"), '{"name":"pkg","main":"index.js"}');
    writeFileSync(join(root, "store/pkg/index.js"), 'module.exports = "from pkg";');
    mkdirSync(join(root, "vendored/node_modules"), { recursive: true });
    symlinkSync("../../store/pkg", join(root, "vendored/node_modules/pkg"), "dir");
    mkdirSync(join(root, "app"));
    symlinkSync(join(root, "vendored/node_modules"), join(root, "app/node_modules"), "dir");
    writeFileSync(join(root, "app/entry.mjs"), 'import said from "pkg";\nconsole.log(said);\n');

    const functionDir = join(root, "function");
    const { entryFile } = await bundleFunction(join(root, "app/entry.mjs"), functionDir, {
      warn: () => {},
    });

    expect(execFileSync("node", [join(functionDir, entryFile)], { encoding: "utf8" })).toBe(
      "from pkg\n",
    );
  });
});

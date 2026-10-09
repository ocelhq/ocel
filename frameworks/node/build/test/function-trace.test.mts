import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { traceIntoFunction } from "../src/function-trace.mjs";
import { BuildOutput, ROOT_FUNCTION } from "../src/output.mjs";

describe("traceIntoFunction", () => {
  it("runs a dependency reached through a node_modules the app links to elsewhere", async () => {
    const root = mkdtempSync(path.join(tmpdir(), "ocel-function-trace-"));
    mkdirSync(path.join(root, "store/pkg"), { recursive: true });
    writeFileSync(path.join(root, "store/pkg/package.json"), '{"name":"pkg","main":"index.js"}');
    writeFileSync(path.join(root, "store/pkg/index.js"), 'module.exports = "from pkg";');
    mkdirSync(path.join(root, "vendored/node_modules"), { recursive: true });
    symlinkSync("../../store/pkg", path.join(root, "vendored/node_modules/pkg"), "dir");
    mkdirSync(path.join(root, "app"));
    symlinkSync(
      path.join(root, "vendored/node_modules"),
      path.join(root, "app/node_modules"),
      "dir",
    );
    writeFileSync(
      path.join(root, "app/entry.mjs"),
      'import said from "pkg";\nconsole.log(said);\n',
    );
    const output = new BuildOutput(path.join(root, "out"), "web");

    const entryFile = await traceIntoFunction(
      output,
      ROOT_FUNCTION,
      path.join(root, "app/entry.mjs"),
      { warn: () => {} },
    );

    expect(entryFile).toBe("app/entry.mjs");
    expect(
      execFileSync("node", [path.join(output.functionDir(ROOT_FUNCTION), entryFile)], {
        encoding: "utf8",
      }),
    ).toBe("from pkg\n");
  });
});

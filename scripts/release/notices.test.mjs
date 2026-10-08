import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, it } from "node:test";
import { licensing, render } from "./notices.mjs";

describe("licensing", () => {
  it("takes the license and notice files beside a package, and nothing else", () => {
    const dir = mkdtempSync(join(tmpdir(), "ocel-notices-"));
    try {
      for (const name of ["LICENSE.md", "NOTICE", "COPYING", "README.md", "license.js"]) {
        writeFileSync(join(dir, name), name);
      }
      mkdirSync(join(dir, "LICENSES"));
      assert.deepEqual(
        licensing(dir).map((file) => file.slice(dir.length + 1)),
        ["COPYING", "LICENSE.md", "NOTICE"],
      );
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});

describe("render", () => {
  const mit = { license: "MIT", text: "MIT text" };
  const apache = { license: "Apache-2.0", text: "Apache text" };

  it("lists every component that shares a text once above that text", () => {
    const out = render([
      { name: "b", version: "1.0.0", ...apache },
      { name: "a", version: "2.0.0", ...apache },
      { name: "c", version: "0.1.0", ...mit },
    ]);
    assert.equal(out.match(/Apache text/g).length, 1);
    assert.match(out, /a 2\.0\.0 \(Apache-2\.0\)\nb 1\.0\.0 \(Apache-2\.0\)\n-+\n\nApache text/);
  });

  it("writes the same bytes whatever order the components come in", () => {
    const components = [
      { name: "z", version: "1.0.0", ...mit },
      { name: "a", version: "1.0.0", ...apache },
      { name: "m", version: "1.0.0", ...mit },
    ];
    assert.equal(render(components), render([...components].reverse()));
  });

  it("lists a component that appears twice once", () => {
    const out = render([
      { name: "a", version: "1.0.0", ...mit },
      { name: "a", version: "1.0.0", ...mit },
    ]);
    assert.equal(out.match(/^a 1\.0\.0/gm).length, 1);
  });
});

describe("THIRD_PARTY_NOTICES", () => {
  it("names no module of this repository", () => {
    const root = join(import.meta.dirname, "..", "..");
    const work = readFileSync(join(root, "go.work"), "utf8");
    const modules = [...work.matchAll(/^\s+(\.\/\S+)$/gm)].map(
      ([, dir]) => /^module\s+(\S+)/m.exec(readFileSync(join(root, dir, "go.mod"), "utf8"))[1],
    );
    const named = readFileSync(join(root, "THIRD_PARTY_NOTICES"), "utf8")
      .split("\n")
      .map((line) => line.split(" ")[0]);
    for (const module of modules) assert.ok(!named.includes(module), module);
  });
});

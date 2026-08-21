import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { afterEach, describe, expect, it } from "vitest";

const assertion = fileURLToPath(
  new URL("../../e2e-next.js/assert-filter-scope.mjs", import.meta.url),
);
const roots: string[] = [];

afterEach(() => {
  for (const root of roots.splice(0)) rmSync(root, { recursive: true });
});

describe("Next e2e filter scope", () => {
  it("accepts a baseline whose suites remain scheduled after upstream exclusions", () => {
    const root = fixture({ baselineSuite: "test/e2e/keep/keep.test.ts" });
    expect(() =>
      run(
        root,
        "test/deploy-tests-manifest.json,test/ocel-deploy-tests-manifest.json",
      ),
    ).not.toThrow();
  });

  it("rejects a chain without the upstream manifest", () => {
    const root = fixture({ baselineSuite: "test/e2e/keep/keep.test.ts" });
    expect(() =>
      run(root, "test/ocel-deploy-tests-manifest.json"),
    ).toThrow(/deploy-tests-manifest\.json is not in the chain/);
  });

  it("rejects baseline entries excluded from the scheduled suite set", () => {
    const root = fixture({ baselineSuite: "test/e2e/skip/skip.test.ts" });
    expect(() =>
      run(
        root,
        "test/deploy-tests-manifest.json,test/ocel-deploy-tests-manifest.json",
      ),
    ).toThrow(/baseline lists never run/);
  });
});

function run(root: string, filters: string) {
  const source = `import { assertFilterScope } from ${JSON.stringify(pathToFileURL(assertion).href)};
console.error(assertFilterScope(process.argv[1], process.argv[2]));`;
  const result = spawnSync(
    process.execPath,
    ["--input-type=module", "--eval", source, root, filters],
    {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  const output = `${result.stdout}${result.stderr}`;
  if (result.status !== 0) throw new Error(output);
  return output;
}

function fixture({ baselineSuite }: { baselineSuite: string }) {
  const root = mkdtempSync(join(tmpdir(), "ocel-filter-scope-"));
  roots.push(root);
  mkdirSync(join(root, "test", "e2e", "keep"), { recursive: true });
  mkdirSync(join(root, "test", "e2e", "skip"), { recursive: true });
  writeFileSync(join(root, "package.json"), '{"type":"commonjs"}\n');
  writeFileSync(join(root, "test", "e2e", "keep", "keep.test.ts"), "");
  writeFileSync(join(root, "test", "e2e", "skip", "skip.test.ts"), "");
  writeFileSync(
    join(root, "test", "deploy-tests-manifest.json"),
    JSON.stringify({ rules: { exclude: ["test/e2e/skip/skip.test.ts"] } }),
  );
  writeFileSync(
    join(root, "test", "ocel-deploy-tests-manifest.json"),
    JSON.stringify({ suites: { [baselineSuite]: { failed: ["case"] } } }),
  );
  writeFileSync(
    join(root, "test", "get-test-filter.js"),
    `exports.mergeManifests = (manifests) => ({
  rules: { exclude: manifests.flatMap((manifest) => manifest.rules?.exclude ?? []) }
});
exports.getTestFilterFromManifest = (manifest) => (tests) =>
  tests.filter((test) => !(manifest.rules?.exclude ?? []).includes(test.file));
`,
  );
  return root;
}

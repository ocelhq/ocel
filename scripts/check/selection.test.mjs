import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { derivesFrom, selectGoModules, selectSetup, touches } from "./selection.mjs";

const modules = [
  {
    dir: "cli",
    path: "example.com/ocel/cli",
    requires: ["example.com/ocel/pkg", "example.com/ocel/aws/runtime"],
  },
  { dir: "pkg", path: "example.com/ocel/pkg", requires: [] },
  {
    dir: "pkg/provider/pulumi",
    path: "example.com/ocel/pkg/provider/pulumi",
    requires: ["example.com/ocel/pkg"],
  },
  {
    dir: "platform/aws/provider",
    path: "example.com/ocel/aws/provider",
    requires: ["example.com/ocel/pkg", "example.com/ocel/pkg/provider/pulumi"],
  },
  {
    dir: "platform/aws/runtime",
    path: "example.com/ocel/aws/runtime",
    requires: ["example.com/ocel/pkg", "example.com/ocel/aws/provider"],
  },
  { dir: "sdk", path: "ocel.dev", requires: ["google.golang.org/protobuf"] },
  { dir: "tests/fixtures/sdk/go", path: "example.com/web", requires: ["ocel.dev"] },
];

const setup = ["cli", "platform/aws/provider", "pkg/provider/transform"];

function dirs(selected) {
  return selected.map((module) => module.dir);
}

describe("selectGoModules", () => {
  it("selects the module a changed file sits in, and nothing that does not require it", () => {
    assert.deepEqual(dirs(selectGoModules(["cli/internal/deploy/deploy.go"], modules)), ["cli"]);
  });

  it("gives the changed file as the reason a module is selected", () => {
    assert.deepEqual(selectGoModules(["cli/a.go", "cli/b.go"], modules), [
      { dir: "cli", reason: "changed cli/a.go and 1 more" },
    ]);
  });

  it("selects the innermost module for a file in a nested module", () => {
    assert.deepEqual(dirs(selectGoModules(["pkg/provider/pulumi/stack.go"], modules)), [
      "cli",
      "pkg/provider/pulumi",
      "platform/aws/provider",
      "platform/aws/runtime",
    ]);
  });

  it("selects every module that requires a changed module, however indirectly", () => {
    assert.deepEqual(selectGoModules(["platform/aws/provider/deploy.go"], modules), [
      { dir: "cli", reason: "requires platform/aws/runtime" },
      { dir: "platform/aws/provider", reason: "changed platform/aws/provider/deploy.go" },
      { dir: "platform/aws/runtime", reason: "requires platform/aws/provider" },
    ]);
  });

  it("follows a module required under a path outside the repository's own", () => {
    assert.deepEqual(dirs(selectGoModules(["sdk/bucket.go"], modules)), [
      "sdk",
      "tests/fixtures/sdk/go",
    ]);
  });

  it("selects no module for files outside every module", () => {
    assert.deepEqual(
      selectGoModules(["README.md", "www/content/docs/index.mdx", "clix/a.go"], modules),
      [],
    );
  });

  it("selects the module whose tests read a file that sits outside every module", () => {
    assert.deepEqual(selectGoModules(["www/content/docs/telemetry.mdx"], modules), [
      { dir: "cli", reason: "changed www/content/docs/telemetry.mdx" },
    ]);
  });

  it("selects every module when the workspace or the lint config changes", () => {
    for (const file of ["go.work", "go.work.sum", ".golangci.yml"]) {
      const selected = selectGoModules([file], modules);
      assert.deepEqual(dirs(selected), dirs(modules).sort());
      assert.ok(selected.every((module) => module.reason === `changed ${file}`));
    }
  });
});

describe("selectSetup", () => {
  it("generates what a selected module and every module it requires embed", () => {
    assert.deepEqual(selectSetup(["cli"], modules, setup), setup);
  });

  it("leaves out what only modules the selection does not require embed", () => {
    assert.deepEqual(selectSetup(["platform/aws/provider"], modules, setup), [
      "platform/aws/provider",
      "pkg/provider/transform",
    ]);
  });

  it("generates nothing for a module that requires no module with embeds", () => {
    assert.deepEqual(selectSetup(["sdk", "tests/fixtures/sdk/go"], modules, setup), []);
  });
});

describe("touches", () => {
  it("is true when a changed file sits under the directory", () => {
    assert.equal(touches(["README.md", "python/ocel/pyproject.toml"], "python"), true);
  });

  it("is false for a sibling whose name only starts the same", () => {
    assert.equal(touches(["pythonic/x.py", "crates.md"], "python"), false);
    assert.equal(touches(["crates.md"], "crates"), false);
  });
});

describe("derivesFrom", () => {
  it("is true for each source a derived file is written from", () => {
    for (const file of [
      "proto/app/v1/app.proto",
      "proto/buf.gen.yaml",
      "VERSION",
      "pkg/configdoc/schema.core.json",
      "platform/aws/provider/schema.provider.json",
      "platform/edge/cloudflare/schema.edge.json",
      "scripts/schema/build.mjs",
      "scripts/check/derived.sh",
      "cli/internal/commands/logs/logs.go",
      "www/content/cli/logs.mdx",
      "package.json",
      "LICENSE",
      "NOTICE",
      "sdk/LICENSE",
      "sdk/NOTICE",
    ]) {
      assert.equal(derivesFrom([file]), true, file);
    }
  });

  it("is true for a derived file itself, which no one edits by hand", () => {
    for (const file of [
      "pkg/proto/app/v1/app.pb.go",
      "pkg/configdoc/selectors.json",
      "www/public/schema/0.1.0/ocel.schema.json",
      "packages/ocel/src/generated/config.ts",
      "www/content/docs/cli/env/set.mdx",
    ]) {
      assert.equal(derivesFrom([file]), true, file);
    }
  });

  it("is false for files nothing derives from", () => {
    assert.equal(derivesFrom(["cli/main.go", "README.md", "packages/ocel/package.json"]), false);
    assert.equal(derivesFrom(["protocol/notes.md", "platform/aws/provider/main.go"]), false);
  });
});

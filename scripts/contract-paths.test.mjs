import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { canChangeContract, findContractPaths } from "./contract-paths.mjs";

describe("findContractPaths", () => {
  it("finds a file in the root provider package", () => {
    assert.deepEqual(findContractPaths(["pkg/provider/hooks.go"]), ["pkg/provider/hooks.go"]);
  });

  it("passes over the provider package's subpackages", () => {
    assert.deepEqual(findContractPaths(["pkg/provider/stackrecords/hostnames.go"]), []);
  });

  it("finds every file under a contract directory", () => {
    const paths = [
      "platform/edge/contract/src/index.ts",
      "proto/provider/contract/v1/contract.proto",
      "packages/ocel/src/index.ts",
      "sdk/env.go",
      "python/ocel/pyproject.toml",
      "crates/ocel-sdk/Cargo.toml",
    ];
    assert.deepEqual(findContractPaths(paths), paths);
  });

  it("finds the rules an agent or a reviewer reads", () => {
    const paths = [".greptile/rules.md", "AGENTS.md", "CLAUDE.md", "CONTRIBUTING.md"];
    assert.deepEqual(findContractPaths(paths), paths);
  });

  it("passes over a path that only shares a contract directory's prefix", () => {
    assert.deepEqual(findContractPaths(["sdkgen/main.go", "platform/edge/contracts.md"]), []);
  });

  it("passes over paths outside the contract", () => {
    assert.deepEqual(findContractPaths(["cli/main.go", "www/README.md", "docs/AGENTS.md"]), []);
  });
});

describe("canChangeContract", () => {
  it("lets an owner, a member and a collaborator change the contract", () => {
    for (const association of ["OWNER", "MEMBER", "COLLABORATOR"]) {
      assert.equal(canChangeContract(association), true, association);
    }
  });

  it("refuses everyone else", () => {
    for (const association of [
      "CONTRIBUTOR",
      "FIRST_TIME_CONTRIBUTOR",
      "FIRST_TIMER",
      "MANNEQUIN",
      "NONE",
      "",
    ]) {
      assert.equal(canChangeContract(association), false, association);
    }
  });
});

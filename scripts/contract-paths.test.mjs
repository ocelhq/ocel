import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { describe, it } from "node:test";
import { fileURLToPath } from "node:url";
import { canChangeContract, isContractPath } from "./contract-paths.mjs";

const script = fileURLToPath(new URL("./contract-paths.mjs", import.meta.url));

const fork = { repository: "ocelhq/ocel", headRepository: "someone/ocel" };
const branch = { repository: "ocelhq/ocel", headRepository: "ocelhq/ocel" };

function runCheck(pullRequest, paths) {
  return spawnSync(process.execPath, [script], {
    input: paths.join("\n"),
    env: {
      PATH: process.env.PATH,
      REPOSITORY: pullRequest.repository,
      HEAD_REPOSITORY: pullRequest.headRepository,
      ASSOCIATION: pullRequest.association,
    },
  });
}

describe("isContractPath", () => {
  it("is true for a Go file in the root provider package", () => {
    assert.equal(isContractPath("pkg/provider/hooks.go"), true);
  });

  it("is false for a file in a provider subpackage", () => {
    assert.equal(isContractPath("pkg/provider/stackrecords/hostnames.go"), false);
  });

  it("is false for a file in the provider package that is not Go", () => {
    assert.equal(isContractPath("pkg/provider/README.md"), false);
  });

  it("is true for every file under a contract directory", () => {
    for (const path of [
      "platform/edge/contract/src/index.ts",
      "proto/provider/contract/v1/contract.proto",
      "packages/ocel/src/index.ts",
      "sdk/env.go",
      "python/ocel/pyproject.toml",
      "crates/ocel-sdk/Cargo.toml",
    ]) {
      assert.equal(isContractPath(path), true, path);
    }
  });

  it("is true for the rules an agent or a reviewer reads", () => {
    for (const path of [".greptile/rules.md", "AGENTS.md", "CLAUDE.md", "CONTRIBUTING.md"]) {
      assert.equal(isContractPath(path), true, path);
    }
  });

  it("is false for a path that only shares a contract directory's prefix", () => {
    for (const path of ["sdkgen/main.go", "platform/edge/contracts.md"]) {
      assert.equal(isContractPath(path), false, path);
    }
  });

  it("is false for a path outside the contract", () => {
    for (const path of ["cli/main.go", "www/README.md", "docs/AGENTS.md"]) {
      assert.equal(isContractPath(path), false, path);
    }
  });
});

describe("canChangeContract", () => {
  it("is true for a branch of this repository, whoever opened it", () => {
    for (const association of ["OWNER", "CONTRIBUTOR", "NONE", ""]) {
      assert.equal(canChangeContract({ ...branch, association }), true, association);
    }
  });

  it("is true for a fork opened by an owner, a member or a collaborator", () => {
    for (const association of ["OWNER", "MEMBER", "COLLABORATOR"]) {
      assert.equal(canChangeContract({ ...fork, association }), true, association);
    }
  });

  it("is false for a fork opened by anyone else", () => {
    for (const association of [
      "CONTRIBUTOR",
      "FIRST_TIME_CONTRIBUTOR",
      "FIRST_TIMER",
      "MANNEQUIN",
      "NONE",
      "",
    ]) {
      assert.equal(canChangeContract({ ...fork, association }), false, association);
    }
  });

  it("is false for a fork whose repository was deleted", () => {
    assert.equal(canChangeContract({ ...fork, headRepository: "", association: "NONE" }), false);
  });
});

describe("contract-paths.mjs", () => {
  it("exits 1 and names the path when a fork from a non-maintainer changes a contract path", () => {
    const run = runCheck({ ...fork, association: "NONE" }, [
      "cli/main.go",
      "proto/provider/contract/v1/contract.proto",
    ]);
    assert.equal(run.status, 1);
    assert.match(run.stderr.toString(), /proto\/provider\/contract\/v1\/contract\.proto/);
  });

  it("exits 0 when a maintainer's fork changes a contract path", () => {
    const run = runCheck({ ...fork, association: "MEMBER" }, ["proto/a.proto"]);
    assert.equal(run.status, 0);
  });

  it("exits 0 when a branch of this repository changes a contract path", () => {
    const run = runCheck({ ...branch, association: "NONE" }, ["proto/a.proto"]);
    assert.equal(run.status, 0);
  });

  it("exits 0 when a fork from a non-maintainer changes no contract path", () => {
    const run = runCheck({ ...fork, association: "NONE" }, ["cli/main.go"]);
    assert.equal(run.status, 0);
  });
});

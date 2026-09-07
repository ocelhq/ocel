import { describe, it } from "bun:test";
import assert from "node:assert/strict";
import { reclaimable, sweepable } from "./slugs";

const CELLS = [
  "deploy-node",
  "deploy-node-container",
  "sdk-node",
  "sdk-node-container",
  "sdk-workspace",
  "sdk-workspace-api-gateway",
];

describe("reclaimable", () => {
  it("reads the cell out of a harness slug", () => {
    assert.deepEqual(reclaimable("j-local-vndaba-deploy-node", CELLS), {
      slug: "j-local-vndaba-deploy-node",
      cell: "deploy-node",
    });
  });

  it("reads the longest cell name a slug ends in, not the shortest", () => {
    assert.deepEqual(reclaimable("j-1874-sdk-node", CELLS), {
      slug: "j-1874-sdk-node",
      cell: "sdk-node",
    });
    assert.deepEqual(reclaimable("j-1874-sdk-node-container", CELLS), {
      slug: "j-1874-sdk-node-container",
      cell: "sdk-node-container",
    });
    assert.deepEqual(reclaimable("j-1874-sdk-workspace-api-gateway", CELLS), {
      slug: "j-1874-sdk-workspace-api-gateway",
      cell: "sdk-workspace-api-gateway",
    });
    assert.deepEqual(reclaimable("j-1874-sdk-workspace", CELLS), {
      slug: "j-1874-sdk-workspace",
      cell: "sdk-workspace",
    });
  });

  it("reads nothing out of a slug no harness run made", () => {
    assert.equal(reclaimable("deploy-node", CELLS), undefined);
    assert.equal(reclaimable("jobs-deploy-node", CELLS), undefined);
  });

  it("claims a harness slug whose cell left the spec table, naming no cell", () => {
    assert.deepEqual(reclaimable("j-1874-nowhere", CELLS), {
      slug: "j-1874-nowhere",
      cell: undefined,
    });
    assert.deepEqual(reclaimable("j-local-apigw3-hello-express", CELLS), {
      slug: "j-local-apigw3-hello-express",
      cell: undefined,
    });
  });

  it("reads the cell out of a harness slug whose run identity came out empty", () => {
    assert.deepEqual(reclaimable("j--deploy-node", CELLS), {
      slug: "j--deploy-node",
      cell: "deploy-node",
    });
  });
});

describe("sweepable", () => {
  it("leaves this run's slugs and everything without the prefix alone", () => {
    const found = [
      "deploy-node",
      "someone-elses-project",
      "j-1874-sdk-node",
      "j-local-vndaba-sdk-node",
    ];
    assert.deepEqual(
      sweepable(found, ["j-1874-sdk-node"], CELLS).map((entry) => entry.slug),
      ["j-local-vndaba-sdk-node"],
    );
  });

  it("reclaims a prefixed slug whose cell left the spec table", () => {
    assert.deepEqual(sweepable(["j-1874-gone"], [], CELLS), [
      { slug: "j-1874-gone", cell: undefined },
    ]);
  });

  it("names a slug listed twice once", () => {
    assert.equal(sweepable(["j-9-sdk-node", "j-9-sdk-node"], [], CELLS).length, 1);
  });
});

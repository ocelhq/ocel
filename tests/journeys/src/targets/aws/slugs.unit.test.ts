import assert from "node:assert/strict";
import { describe, it } from "bun:test";
import { reclaimable, sweepable } from "./slugs";

const CELLS = [
  "deploy-express",
  "deploy-express-container",
  "sdk-express",
  "sdk-express-container",
  "sdk-workspace",
  "sdk-workspace-api-gateway",
];

describe("reclaimable", () => {
  it("reads the cell out of a harness slug", () => {
    assert.deepEqual(reclaimable("j-local-vndaba-deploy-express", CELLS), {
      slug: "j-local-vndaba-deploy-express",
      cell: "deploy-express",
    });
  });

  it("reads the longest cell name a slug ends in, not the shortest", () => {
    assert.deepEqual(reclaimable("j-1874-sdk-express", CELLS), {
      slug: "j-1874-sdk-express",
      cell: "sdk-express",
    });
    assert.deepEqual(reclaimable("j-1874-sdk-express-container", CELLS), {
      slug: "j-1874-sdk-express-container",
      cell: "sdk-express-container",
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
    assert.equal(reclaimable("deploy-express", CELLS), undefined);
    assert.equal(reclaimable("jobs-deploy-express", CELLS), undefined);
    assert.equal(reclaimable("j--deploy-express", CELLS), undefined);
  });

  it("reads nothing out of a harness slug naming a cell nobody has", () => {
    assert.equal(reclaimable("j-1874-nowhere", CELLS), undefined);
  });
});

describe("sweepable", () => {
  it("leaves this run's slugs and everything without the prefix alone", () => {
    const found = [
      "deploy-express",
      "someone-elses-project",
      "j-1874-sdk-express",
      "j-local-vndaba-sdk-express",
    ];
    const { reclaim, unreadable } = sweepable(found, ["j-1874-sdk-express"], CELLS);
    assert.deepEqual(
      reclaim.map((entry) => entry.slug),
      ["j-local-vndaba-sdk-express"],
    );
    assert.deepEqual(unreadable, []);
  });

  it("reports a prefixed slug it cannot place rather than reclaiming it", () => {
    const { reclaim, unreadable } = sweepable(["j-1874-gone"], [], CELLS);
    assert.deepEqual(reclaim, []);
    assert.deepEqual(unreadable, ["j-1874-gone"]);
  });

  it("names a slug listed twice once", () => {
    const { reclaim } = sweepable(["j-9-sdk-express", "j-9-sdk-express"], [], CELLS);
    assert.equal(reclaim.length, 1);
  });
});

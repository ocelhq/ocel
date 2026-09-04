import assert from "node:assert/strict";
import { describe, it } from "vitest";
import { reclaimable, sweepable } from "./slugs";

const CELLS = [
  "express",
  "express-hello",
  "express-container",
  "express-hello-container",
  "workspace",
  "workspace-hello",
  "workspace-hello-api-gateway",
  "workspace-hello-api-gateway-container",
];

describe("reclaimable", () => {
  it("reads the example out of a harness slug", () => {
    assert.deepEqual(reclaimable("j-local-vndaba-express", CELLS), {
      slug: "j-local-vndaba-express",
      example: "express",
    });
  });

  it("reads the longest cell name a slug ends in, not the shortest", () => {
    assert.deepEqual(reclaimable("j-1874-express-hello", CELLS), {
      slug: "j-1874-express-hello",
      example: "express-hello",
    });
    assert.deepEqual(reclaimable("j-1874-express-hello-container", CELLS), {
      slug: "j-1874-express-hello-container",
      example: "express-hello-container",
    });
    assert.deepEqual(reclaimable("j-1874-workspace-hello-api-gateway-container", CELLS), {
      slug: "j-1874-workspace-hello-api-gateway-container",
      example: "workspace-hello-api-gateway-container",
    });
    assert.deepEqual(reclaimable("j-1874-workspace-hello-api-gateway", CELLS), {
      slug: "j-1874-workspace-hello-api-gateway",
      example: "workspace-hello-api-gateway",
    });
  });

  it("reads nothing out of a slug no harness run made", () => {
    assert.equal(reclaimable("express", CELLS), undefined);
    assert.equal(reclaimable("jobs-express", CELLS), undefined);
    assert.equal(reclaimable("j--express", CELLS), undefined);
  });

  it("reads nothing out of a harness slug naming an example nobody has", () => {
    assert.equal(reclaimable("j-1874-nowhere", CELLS), undefined);
  });
});

describe("sweepable", () => {
  it("leaves this run's slugs and everything without the prefix alone", () => {
    const found = [
      "express",
      "someone-elses-project",
      "j-1874-express",
      "j-local-vndaba-express",
    ];
    const { reclaim, unreadable } = sweepable(found, ["j-1874-express"], CELLS);
    assert.deepEqual(
      reclaim.map((entry) => entry.slug),
      ["j-local-vndaba-express"],
    );
    assert.deepEqual(unreadable, []);
  });

  it("reports a prefixed slug it cannot place rather than reclaiming it", () => {
    const { reclaim, unreadable } = sweepable(["j-1874-gone"], [], CELLS);
    assert.deepEqual(reclaim, []);
    assert.deepEqual(unreadable, ["j-1874-gone"]);
  });

  it("names a slug listed twice once", () => {
    const { reclaim } = sweepable(["j-9-express", "j-9-express"], [], CELLS);
    assert.equal(reclaim.length, 1);
  });
});

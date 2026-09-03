import assert from "node:assert/strict";
import { describe, it } from "vitest";
import { reclaimable, sweepable } from "./slugs";

const EXAMPLES = ["express", "hello-express"];

describe("reclaimable", () => {
  it("reads the run and the example out of a harness slug", () => {
    assert.deepEqual(reclaimable("j-local-vndaba-express", EXAMPLES), {
      slug: "j-local-vndaba-express",
      run: "local-vndaba",
      example: "express",
    });
  });

  it("reads a run id that is only digits", () => {
    assert.deepEqual(reclaimable("j-1874-hello-express", EXAMPLES), {
      slug: "j-1874-hello-express",
      run: "1874",
      example: "hello-express",
    });
  });

  it("reads nothing out of a slug no harness run made", () => {
    assert.equal(reclaimable("express", EXAMPLES), undefined);
    assert.equal(reclaimable("jobs-express", EXAMPLES), undefined);
    assert.equal(reclaimable("j--express", EXAMPLES), undefined);
  });

  it("reads nothing out of a harness slug naming an example nobody has", () => {
    assert.equal(reclaimable("j-1874-nowhere", EXAMPLES), undefined);
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
    const { reclaim, unreadable } = sweepable(found, ["j-1874-express"], EXAMPLES);
    assert.deepEqual(
      reclaim.map((entry) => entry.slug),
      ["j-local-vndaba-express"],
    );
    assert.deepEqual(unreadable, []);
  });

  it("reports a prefixed slug it cannot place rather than reclaiming it", () => {
    const { reclaim, unreadable } = sweepable(["j-1874-gone"], [], EXAMPLES);
    assert.deepEqual(reclaim, []);
    assert.deepEqual(unreadable, ["j-1874-gone"]);
  });

  it("names a slug listed twice once", () => {
    const { reclaim } = sweepable(["j-9-express", "j-9-express"], [], EXAMPLES);
    assert.equal(reclaim.length, 1);
  });
});

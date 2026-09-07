import { describe, it } from "bun:test";
import assert from "node:assert/strict";
import { harnessStagesIn } from "./ladder-sst";

describe("harnessStagesIn", () => {
  it("names every harness stage the SST home records, whoever ran it", () => {
    assert.deepEqual(
      harnessStagesIn([
        "app/with-sst/j-local-ag.json",
        "app/with-sst/j-local-vndaba.json",
        "app/with-sst/j-33944052992.json",
        "app/with-sst/staging.json",
      ]),
      ["j-local-ag", "j-local-vndaba", "j-33944052992"],
    );
  });

  it("finds nothing in an empty home", () => {
    assert.deepEqual(harnessStagesIn([]), []);
  });
});

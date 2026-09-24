import { describe, it } from "bun:test";
import assert from "node:assert/strict";
import { harnessStagesIn, sstEnv, staleStages } from "./sst";

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

describe("staleStages", () => {
  it("removes the sweep's own stage and every recorded one no live run is using", () => {
    assert.deepEqual(staleStages("1900", ["j-1874", "j-1875", "j-local-ag"], new Set(["j-1875"])), [
      "j-1900",
      "j-1874",
      "j-local-ag",
    ]);
  });
});

describe("sstEnv", () => {
  it("points sst at the endpoint of the world the stack was given, whatever the process says", async () => {
    const env = await sstEnv(
      { endpoint: async () => "http://floci.test:4566" },
      { AWS_ENDPOINT_URL: "http://elsewhere.test:4566" },
    );
    assert.equal(env.AWS_ENDPOINT_URL, "http://floci.test:4566");
  });

  it("leaves sst on real aws when the world has no endpoint", async () => {
    const env = await sstEnv({ endpoint: async () => undefined }, { HOME: "/home/journey" });
    assert.deepEqual(env, { HOME: "/home/journey" });
  });
});

import { describe, expect, it } from "bun:test";
import { filterFrom } from "./filter";

describe("the concerns a run asks for", () => {
  it("is every concern but iac when nothing names one", () => {
    expect(filterFrom({}).concerns).toEqual([
      "deploy",
      "lifecycle",
      "sdk",
      "build-variables",
      "prerender",
      "pre-build",
      "kv",
      "tasks",
      "realtime",
    ]);
    expect(filterFrom({ OCEL_E2E_CONCERN: "  " }).concerns).toEqual([
      "deploy",
      "lifecycle",
      "sdk",
      "build-variables",
      "prerender",
      "pre-build",
      "kv",
      "tasks",
      "realtime",
    ]);
  });

  it("takes a space- or comma-separated naming in concern order", () => {
    expect(filterFrom({ OCEL_E2E_CONCERN: "sdk deploy" }).concerns).toEqual(["deploy", "sdk"]);
    expect(filterFrom({ OCEL_E2E_CONCERN: "deploy,sdk" }).concerns).toEqual(["deploy", "sdk"]);
    expect(filterFrom({ OCEL_E2E_CONCERN: "lifecycle" }).concerns).toEqual(["lifecycle"]);
  });

  it("runs iac only when it is named", () => {
    expect(filterFrom({ OCEL_E2E_CONCERN: "iac" }).concerns).toEqual(["iac"]);
    expect(filterFrom({ OCEL_E2E_CONCERN: "iac sdk deploy" }).concerns).toEqual([
      "deploy",
      "sdk",
      "iac",
    ]);
  });

  it("refuses a name that is no concern", () => {
    expect(() => filterFrom({ OCEL_E2E_CONCERN: "deploy console" })).toThrow(
      /console is no concern/,
    );
  });
});

describe("the fixtures and variants a run narrows to", () => {
  it("narrows to nothing when nothing is named", () => {
    expect(filterFrom({ OCEL_E2E_FIXTURES: " , ", OCEL_E2E_VARIANTS: " , " })).toMatchObject({
      fixtures: [],
      variants: [],
    });
  });

  it("takes fixtures and variants separated by spaces or commas", () => {
    const filter = filterFrom({
      OCEL_E2E_FIXTURES: " sdk/node deploy/next,kv/node ",
      OCEL_E2E_VARIANTS: "default cloudflare,container",
    });
    expect(filter.fixtures).toEqual(["sdk/node", "deploy/next", "kv/node"]);
    expect(filter.variants).toEqual(["default", "cloudflare", "container"]);
  });
});

describe("the coverage a run asks for", () => {
  it("covers rather than runs every cell when nothing says", () => {
    expect(filterFrom({}).coverage).toBe("sampled");
    expect(filterFrom({ OCEL_E2E_COVERAGE: " " }).coverage).toBe("sampled");
  });

  it("takes the coverage it is handed", () => {
    expect(filterFrom({ OCEL_E2E_COVERAGE: "every-cell" }).coverage).toBe("every-cell");
    expect(filterFrom({ OCEL_E2E_COVERAGE: "sampled" }).coverage).toBe("sampled");
  });

  it("refuses a coverage nobody runs", () => {
    expect(() => filterFrom({ OCEL_E2E_COVERAGE: "some" })).toThrow(/OCEL_E2E_COVERAGE is some/);
  });

  it("refuses full and covering, which every-cell and sampled replaced", () => {
    expect(() => filterFrom({ OCEL_E2E_COVERAGE: "full" })).toThrow(
      /OCEL_E2E_COVERAGE is full, and a journey runs every-cell or sampled/,
    );
    expect(() => filterFrom({ OCEL_E2E_COVERAGE: "covering" })).toThrow(
      /OCEL_E2E_COVERAGE is covering, and a journey runs every-cell or sampled/,
    );
  });

  it("draws with the seed and the touched fixtures it is handed, and without one when no seed is", () => {
    expect(filterFrom({ OCEL_E2E_TOUCHED: "sdk/node" }).draw).toBeUndefined();
    expect(
      filterFrom({ OCEL_E2E_SEED: " 42 ", OCEL_E2E_TOUCHED: "sdk/node, deploy/go" }).draw,
    ).toEqual({ seed: "42", touched: ["sdk/node", "deploy/go"] });
  });
});

describe("lifting the skips", () => {
  it("keeps them unless told to run", () => {
    expect(filterFrom({}).runSkipped).toBe(false);
    expect(filterFrom({ OCEL_E2E_SKIPS: "skip" }).runSkipped).toBe(false);
    expect(filterFrom({ OCEL_E2E_SKIPS: "run" }).runSkipped).toBe(true);
  });

  it("refuses a word it does not know", () => {
    expect(() => filterFrom({ OCEL_E2E_SKIPS: "maybe" })).toThrow(/OCEL_E2E_SKIPS is maybe/);
  });
});

describe("a run that leaves its cells deployed", () => {
  it("destroys unless it is asked to keep", () => {
    expect(filterFrom({}).keep).toBe(false);
    expect(filterFrom({ OCEL_E2E_KEEP: "  " }).keep).toBe(false);
  });

  it("keeps on every word for yes", () => {
    for (const asked of ["1", "true", "TRUE", "yes", " Yes "]) {
      expect(filterFrom({ OCEL_E2E_KEEP: asked }).keep).toBe(true);
    }
  });

  it("refuses a word it does not know", () => {
    expect(() => filterFrom({ OCEL_E2E_KEEP: "maybe" })).toThrow(/OCEL_E2E_KEEP is maybe/);
    expect(() => filterFrom({ OCEL_E2E_KEEP: "0" })).toThrow(/OCEL_E2E_KEEP is 0/);
  });
});

import { describe, expect, it } from "bun:test";
import { askFrom } from "./ask";

describe("the concerns a run asks for", () => {
  it("is every concern when nothing names one", () => {
    expect(askFrom({}).concerns).toEqual(["deploy", "lifecycle", "sdk"]);
    expect(askFrom({ OCEL_JOURNEY_CONCERN: "  " }).concerns).toEqual([
      "deploy",
      "lifecycle",
      "sdk",
    ]);
  });

  it("takes a space- or comma-separated naming in concern order", () => {
    expect(askFrom({ OCEL_JOURNEY_CONCERN: "sdk deploy" }).concerns).toEqual(["deploy", "sdk"]);
    expect(askFrom({ OCEL_JOURNEY_CONCERN: "deploy,sdk" }).concerns).toEqual(["deploy", "sdk"]);
    expect(askFrom({ OCEL_JOURNEY_CONCERN: "lifecycle" }).concerns).toEqual(["lifecycle"]);
  });

  it("refuses a name that is no concern", () => {
    expect(() => askFrom({ OCEL_JOURNEY_CONCERN: "deploy console" })).toThrow(
      /console is no concern/,
    );
  });
});

describe("the fixtures and variants a run narrows to", () => {
  it("narrows to nothing when nothing is named", () => {
    expect(askFrom({ OCEL_JOURNEY_FIXTURES: " , ", OCEL_JOURNEY_VARIANTS: " , " })).toMatchObject({
      fixtures: [],
      variants: [],
    });
  });

  it("takes comma-separated fixtures and space- or comma-separated variants", () => {
    const ask = askFrom({
      OCEL_JOURNEY_FIXTURES: " sdk/node,deploy/next ",
      OCEL_JOURNEY_VARIANTS: "base cloudflare,container",
    });
    expect(ask.fixtures).toEqual(["sdk/node", "deploy/next"]);
    expect(ask.variants).toEqual(["base", "cloudflare", "container"]);
  });
});

describe("the coverage a run asks for", () => {
  it("covers rather than runs every cell when nothing says", () => {
    expect(askFrom({}).coverage).toBe("covering");
    expect(askFrom({ OCEL_JOURNEY_COVERAGE: " " }).coverage).toBe("covering");
  });

  it("takes the coverage it is handed", () => {
    expect(askFrom({ OCEL_JOURNEY_COVERAGE: "full" }).coverage).toBe("full");
    expect(askFrom({ OCEL_JOURNEY_COVERAGE: "covering" }).coverage).toBe("covering");
  });

  it("refuses a coverage nobody runs", () => {
    expect(() => askFrom({ OCEL_JOURNEY_COVERAGE: "some" })).toThrow(
      /OCEL_JOURNEY_COVERAGE is some/,
    );
  });

  it("draws with the seed and the touched fixtures it is handed, and without one when no seed is", () => {
    expect(askFrom({ OCEL_JOURNEY_TOUCHED: "sdk/node" }).draw).toBeUndefined();
    expect(
      askFrom({ OCEL_JOURNEY_SEED: " 42 ", OCEL_JOURNEY_TOUCHED: "sdk/node, deploy/go" }).draw,
    ).toEqual({ seed: "42", touched: ["sdk/node", "deploy/go"] });
  });
});

describe("lifting the skips", () => {
  it("keeps them unless told to run", () => {
    expect(askFrom({}).runSkipped).toBe(false);
    expect(askFrom({ OCEL_JOURNEY_SKIPS: "skip" }).runSkipped).toBe(false);
    expect(askFrom({ OCEL_JOURNEY_SKIPS: "run" }).runSkipped).toBe(true);
  });

  it("refuses a word it does not know", () => {
    expect(() => askFrom({ OCEL_JOURNEY_SKIPS: "maybe" })).toThrow(/OCEL_JOURNEY_SKIPS is maybe/);
  });
});

describe("a run that leaves its cells standing", () => {
  it("destroys unless it is asked to keep", () => {
    expect(askFrom({}).keep).toBe(false);
    expect(askFrom({ OCEL_JOURNEY_KEEP: "  " }).keep).toBe(false);
  });

  it("keeps on every word for yes", () => {
    for (const asked of ["1", "true", "TRUE", "yes", " Yes "]) {
      expect(askFrom({ OCEL_JOURNEY_KEEP: asked }).keep).toBe(true);
    }
  });

  it("refuses a word it does not know", () => {
    expect(() => askFrom({ OCEL_JOURNEY_KEEP: "maybe" })).toThrow(/OCEL_JOURNEY_KEEP is maybe/);
    expect(() => askFrom({ OCEL_JOURNEY_KEEP: "0" })).toThrow(/OCEL_JOURNEY_KEEP is 0/);
  });
});

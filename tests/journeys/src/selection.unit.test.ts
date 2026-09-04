import { describe, expect, it } from "bun:test";
import { selectionFor } from "./selection";
import { cellNameOf } from "./spec";
import { targetNamed } from "./targets";

const AWS = targetNamed("aws");

function cellsOf(env: NodeJS.ProcessEnv): string[] {
  const { examples, naming, covered } = selectionFor(AWS, env);
  return examples.flatMap((example) =>
    (covered.get(example.name) ?? []).map((variant) => cellNameOf(example, variant, naming)),
  );
}

describe("what a run names and what it covers", () => {
  it("names a cell after the target's whole offer, narrowed or not", () => {
    const whole = { OCEL_TARGET: "aws", OCEL_EXAMPLES: "express", OCEL_JOURNEY_COVERAGE: "full" };
    expect(cellsOf(whole)).toContain("express-api-gateway");
    expect(cellsOf({ ...whole, OCEL_JOURNEY_EDGES: "api-gateway" })).toContain(
      "express-api-gateway",
    );
  });

  it("enumerates only the variants the narrowed offer holds", () => {
    const narrowed = cellsOf({
      OCEL_TARGET: "aws",
      OCEL_EXAMPLES: "express",
      OCEL_JOURNEY_COVERAGE: "full",
      OCEL_JOURNEY_EDGES: "api-gateway",
      OCEL_JOURNEY_COMPUTES: "container",
    });
    expect(narrowed).toEqual([
      "express-api-gateway-container",
      "express-hello-api-gateway-container",
    ]);
  });

  it("hands the target's whole offer back for naming", () => {
    const { naming } = selectionFor(AWS, { OCEL_TARGET: "aws", OCEL_JOURNEY_EDGES: "cloudflare" });
    expect(naming.edges).toEqual(AWS.edges);
    expect(naming.computes).toEqual(AWS.computes);
  });
});

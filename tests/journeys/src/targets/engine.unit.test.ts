import { describe, expect, it } from "bun:test";
import { adoptionMissed, ENGINE_ENV, engineSeries } from "./engine";

describe("engineSeries", () => {
  it("leaves the box's docker to bootstrap when the run names none", () => {
    expect(engineSeries({})).toBeUndefined();
    expect(engineSeries({ [ENGINE_ENV]: " " })).toBeUndefined();
  });

  it("reads the series a lane preinstalls", () => {
    expect(engineSeries({ [ENGINE_ENV]: "28.0" })).toBe("28.0");
  });

  it("refuses a value that names no series rather than asserting against it", () => {
    expect(() => engineSeries({ [ENGINE_ENV]: "latest" })).toThrow(/release series/);
  });
});

describe("adoptionMissed", () => {
  const adopted =
    "    = adopt docker  docker:engine   — docker 28.0.4, not managed by ocel: upgrading it is yours\n";

  it("passes a plan that adopts the docker of the series the box was given", () => {
    expect(adoptionMissed(adopted, "28.0")).toBeUndefined();
  });

  it("reads the row through the colour a terminal paints it in", () => {
    const painted =
      "    = adopt docker  \u001b[2mdocker:engine\u001b[0m\u001b[2m   — docker 28.0.4, not managed by ocel: upgrading it is yours\u001b[0m\n";
    expect(adoptionMissed(painted, "28.0")).toBeUndefined();
  });

  it("names the engine row a plan showed instead", () => {
    const installed =
      "    + docker  docker:engine   — docker 29.8.0, installed once; upgrading it is yours from then on\n";
    expect(adoptionMissed(installed, "28.0")).toContain("docker 29.8.0, installed once");
  });

  it("does not read another series as the one the box was given", () => {
    expect(adoptionMissed(adopted.replace("28.0.4", "28.10.1"), "28.0")).toBeDefined();
  });

  it("says so when the plan showed no engine at all", () => {
    expect(adoptionMissed("2 unchanged.\n", "28.0")).toContain("no engine row at all");
  });
});

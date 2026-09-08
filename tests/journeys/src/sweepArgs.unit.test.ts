import { describe, expect, it } from "bun:test";
import { sweepAsk } from "./sweepArgs";

describe("sweepAsk", () => {
  it("reaps everything no run is using when only a target is named", () => {
    expect(sweepAsk(["--target", "aws"], "34220477331")).toEqual({
      target: "aws",
      runId: "34220477331",
      oneRun: false,
    });
  });

  it("reaps only the run it runs under when asked", () => {
    expect(sweepAsk(["--target", "aws", "--own"], "34220477331")).toEqual({
      target: "aws",
      runId: "34220477331",
      oneRun: true,
    });
  });

  it("reaps only the run named rather than the one it runs under", () => {
    expect(sweepAsk(["--target", "aws", "--run", "34214860916"], "34220477331")).toEqual({
      target: "aws",
      runId: "34214860916",
      oneRun: true,
    });
  });

  it("refuses an ask that names no target", () => {
    expect(() => sweepAsk(["--own"], "1")).toThrow("--target");
  });

  it("refuses a run with no id behind it", () => {
    expect(() => sweepAsk(["--target", "aws", "--run", "--own"], "1")).toThrow(
      "--run needs a value",
    );
  });
});

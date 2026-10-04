import { describe, expect, it } from "bun:test";
import { sweepArgs } from "./sweepArgs";

describe("sweepArgs", () => {
  it("reaps everything no run is using when only a target is named", () => {
    expect(sweepArgs(["--target", "aws"], "34220477331")).toEqual({
      target: "aws",
      runId: "34220477331",
      oneRun: false,
    });
  });

  it("reaps only the run it runs under when asked", () => {
    expect(sweepArgs(["--target", "aws", "--own"], "34220477331")).toEqual({
      target: "aws",
      runId: "34220477331",
      oneRun: true,
    });
  });

  it("reaps only the run named rather than the one it runs under", () => {
    expect(sweepArgs(["--target", "aws", "--run", "34214860916"], "34220477331")).toEqual({
      target: "aws",
      runId: "34214860916",
      oneRun: true,
    });
  });

  it("refuses arguments that name no target", () => {
    expect(() => sweepArgs(["--own"], "1")).toThrow("--target");
  });

  it("refuses a run with no id behind it", () => {
    expect(() => sweepArgs(["--target", "aws", "--run", "--own"], "1")).toThrow(
      "--run needs a value",
    );
  });
});

import { describe, expect, it } from "vitest";
import { stepTab } from "./install-command";

describe("stepTab", () => {
  it("moves right and left through the methods", () => {
    expect(stepTab(0, "ArrowRight", 3)).toBe(1);
    expect(stepTab(1, "ArrowLeft", 3)).toBe(0);
  });

  it("wraps past either end", () => {
    expect(stepTab(2, "ArrowRight", 3)).toBe(0);
    expect(stepTab(0, "ArrowLeft", 3)).toBe(2);
  });

  it("goes to the first and last on Home and End", () => {
    expect(stepTab(1, "Home", 3)).toBe(0);
    expect(stepTab(1, "End", 3)).toBe(2);
  });

  it("ignores any other key", () => {
    expect(stepTab(1, "Enter", 3)).toBeUndefined();
  });
});

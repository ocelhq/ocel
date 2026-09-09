import { describe, expect, it } from "vitest";
import { alb } from "./edge.js";

describe("alb", () => {
  it("names the edge the gcp provider registers", () => {
    expect(alb()).toEqual({ kind: "alb" });
  });
});

import { describe, expect, it } from "bun:test";
import { devProject } from "./dev";

describe("the project a dev stack is labelled with", () => {
  it("is the name ocel dev derives from the directory, so the harness can find what it started", () => {
    expect(devProject("/work/trees/sdk-node/My Shop")).toBe("my-shop-2d2cdb07");
  });

  it("differs between two trees whose last segment is the same", () => {
    expect(devProject("/work/a/node")).not.toBe(devProject("/work/b/node"));
  });
});

import { describe, expect, it } from "vitest";
import * as entry from "./index.js";

describe("what @ocel/sst exports", () => {
  it("is bind, and adding a type is adding a function to it", () => {
    expect(Object.keys(entry)).toEqual(["bind"]);
    expect(Object.keys(entry.bind)).toEqual(["postgres", "custom"]);
  });

  it("offers no bucket, because the runtime serves only ocel-provisioned buckets", () => {
    expect(entry.bind).not.toHaveProperty("bucket");
  });
});

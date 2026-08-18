import { describe, expect, it } from "vitest";
import * as entry from "./index.js";

describe("what @ocel/pulumi exports", () => {
  it("is link, and adding a type is adding a function to it", () => {
    expect(Object.keys(entry)).toEqual(["link"]);
    expect(Object.keys(entry.link)).toEqual(["postgres", "custom"]);
  });

  it("offers no bucket, because the membrane serves only ocel-provisioned buckets", () => {
    expect(entry.link).not.toHaveProperty("bucket");
  });
});

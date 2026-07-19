import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { tagSnapshotKey, type TagSnapshot } from "@ocel/next-cache";
import { describe, expect, it } from "vitest";

import { mergeSnapshot, snapshotValidityMs } from "../src/next/tag-snapshot.mjs";

// The genesis snapshot is written by the Go deploy and rewritten thereafter by
// this publisher, so no type is shared across the boundary and nothing but this
// fixture stops the two from drifting apart in silence. The deploy's own test
// asserts it marshals exactly these bytes; the assertions here are what that
// pins it to. Changing the shape or the validity window must break both sides.
const fixture: TagSnapshot = JSON.parse(
  readFileSync(
    fileURLToPath(
      new URL("../../next-cache/fixtures/genesis-tag-snapshot.json", import.meta.url),
    ),
    "utf8",
  ),
);

describe("the deploy's genesis snapshot", () => {
  it("carries exactly the fields the publisher reads", () => {
    expect(Object.keys(fixture).sort()).toEqual([
      "deployedAt",
      "generatedAt",
      "records",
      "validUntil",
      "version",
    ]);
    expect(fixture.version).toBe(1);
    expect(fixture.records).toEqual({});
  });

  it("declares the trust window this publisher republishes on", () => {
    expect(fixture.validUntil).toBe(fixture.generatedAt + snapshotValidityMs);
  });

  it("anchors pruning, and the publisher carries that anchor forward", () => {
    expect(fixture.deployedAt).toBeGreaterThan(0);
    expect(mergeSnapshot(fixture, new Map(), fixture.generatedAt + 1).deployedAt).toBe(
      fixture.deployedAt,
    );
  });

  it("is addressed at the key the deploy writes it to", () => {
    expect(tagSnapshotKey("prod/proj/web/BID")).toBe("prod/proj/web/BID/tag-clock.json");
  });
});

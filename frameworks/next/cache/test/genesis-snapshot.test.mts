import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { mergeSnapshot, tagSnapshotKey, type TagSnapshot } from "../src/index.mjs";

const fixture: TagSnapshot = JSON.parse(
  readFileSync(
    fileURLToPath(new URL("../fixtures/genesis-tag-snapshot.json", import.meta.url)),
    "utf8",
  ),
);

describe("the deploy's genesis snapshot", () => {
  it("carries exactly the fields the publisher reads", () => {
    expect(Object.keys(fixture).sort()).toEqual([
      "deployedAt",
      "generatedAt",
      "records",
      "version",
    ]);
    expect(fixture.version).toBe(1);
    expect(fixture.records).toEqual({});
  });

  it("carries no expiry for a reader to second-guess the publisher with", () => {
    expect(fixture).not.toHaveProperty("validUntil");
    expect(mergeSnapshot(fixture, new Map(), 1)).not.toHaveProperty("validUntil");
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

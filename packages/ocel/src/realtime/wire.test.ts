import { describe, expect, it } from "vitest";
import { readRealtimeVectors } from "../testing/realtime-vectors.js";
import { encodeWireChannel, parseChannelPattern } from "./wire.js";

describe("the wire channel of a pattern and its params", () => {
  for (const vector of readRealtimeVectors().wire) {
    it(`matches the shared vector: ${vector.name}`, () => {
      const pattern = parseChannelPattern(vector.pattern);

      const encoded = encodeWireChannel(vector.namespace, pattern, vector.params, vector.wildcard);

      expect(encoded).toEqual(
        vector.error ? { refused: vector.error } : { channel: vector.channel },
      );
    });
  }
});

describe("a channel pattern", () => {
  it.each([
    ["", "empty"],
    ["a/b/c/d/e", "at most 4"],
    ["orders//x", "empty segment"],
    ["orders/:1x", "no parameter"],
    ["orders_x", "no literal"],
    ["rooms/:id/:id", "twice"],
  ])("refuses %j", (written, reason) => {
    expect(() => parseChannelPattern(written)).toThrow(reason);
  });
});

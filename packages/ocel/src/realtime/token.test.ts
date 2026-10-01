import { describe, expect, it } from "vitest";
import { readRealtimeVectors } from "../testing/realtime-vectors.js";
import { signToken } from "./token.js";

describe("a minted token", () => {
  const { signingKey, mint } = readRealtimeVectors().tokens;

  for (const vector of mint) {
    it(`is the shared vector's token byte for byte: ${vector.name}`, () => {
      const token = signToken(Buffer.from(signingKey, "base64"), vector.header, vector.claims);

      expect(token).toBe(vector.token);
    });
  }
});

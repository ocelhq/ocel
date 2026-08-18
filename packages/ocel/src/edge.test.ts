import { describe, expect, it } from "vitest";

import { cfEdge } from "./edge.js";

const roundTrip = (value: unknown) => JSON.parse(JSON.stringify(value));

describe("cfEdge", () => {
  it("serialises to the cloudflare edge descriptor", () => {
    expect(roundTrip(cfEdge())).toEqual({ kind: "cloudflare", options: {} });
  });
});

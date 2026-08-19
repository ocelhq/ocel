import { describe, expect, it } from "vitest";

import { cloudflare } from "./edge.js";

const roundTrip = (value: unknown) => JSON.parse(JSON.stringify(value));

describe("cloudflare", () => {
  it("serialises to the cloudflare edge descriptor", () => {
    expect(roundTrip(cloudflare())).toEqual({ kind: "cloudflare", options: {} });
  });
});

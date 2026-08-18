import { describe, expect, it } from "vitest";

import { cloudflareDns } from "./dns.js";

const roundTrip = (value: unknown) => JSON.parse(JSON.stringify(value));

describe("cloudflareDns", () => {
  it("serialises without a zone when none is named", () => {
    expect(roundTrip(cloudflareDns())).toEqual({ kind: "cloudflare" });
  });

  it("serialises the zone it is given", () => {
    expect(roundTrip(cloudflareDns({ zone: "acme.com" }))).toEqual({
      kind: "cloudflare",
      zone: "acme.com",
    });
  });
});

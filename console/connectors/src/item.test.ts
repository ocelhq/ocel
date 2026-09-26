import { describe, expect, it } from "vitest";
import { REFUSALS, refuse, statusOf } from "./item";

describe("statusOf", () => {
  it("answers 403 for a connector that denied the scope", () => {
    const refused = refuse("denied", "the token has no envvars.write scope");
    expect(refused.done).toBe(false);
    if (!refused.done) {
      expect(statusOf(refused.refusal)).toBe(403);
    }
  });

  it("answers 500 for a connector that refused the console's own token", () => {
    const refused = refuse("unauthenticated", "the connector refused the console's token");
    expect(refused.done).toBe(false);
    if (!refused.done) {
      expect(statusOf(refused.refusal)).toBe(500);
    }
  });

  it("answers 502 for every other refusal, because the console reached nothing useful", () => {
    const own = ["denied", "unauthenticated"];
    for (const reason of REFUSALS.filter((other) => !own.includes(other))) {
      expect(statusOf({ reason, message: "" })).toBe(502);
    }
  });
});

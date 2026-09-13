import { describe, expect, it } from "vitest";
import { REFUSALS, refuse, statusOf } from "./item";

describe("statusOf", () => {
  it("answers 403 for a connector that denied the scope", () => {
    const refused = refuse("denied", "the token carries no envvars.write scope");
    expect(refused.done).toBe(false);
    if (!refused.done) {
      expect(statusOf(refused.refusal)).toBe(403);
    }
  });

  it("answers 502 for every other refusal, because the console reached nothing useful", () => {
    for (const reason of REFUSALS.filter((held) => held !== "denied")) {
      expect(statusOf({ reason, message: "" })).toBe(502);
    }
  });
});

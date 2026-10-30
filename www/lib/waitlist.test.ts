import { describe, expect, it } from "vitest";
import { waitlistEvent } from "./waitlist";

describe("waitlistEvent", () => {
  it("records an email as a PostHog person on the waitlist", () => {
    expect(
      waitlistEvent({ email: " Ada@Example.com ", source: "landing", company: "" }, "phc_x"),
    ).toEqual({
      api_key: "phc_x",
      event: "waitlist_joined",
      distinct_id: "ada@example.com",
      properties: {
        source: "landing",
        $set: { email: "ada@example.com" },
        $set_once: { waitlist_source: "landing" },
      },
    });
  });

  it("refuses what is not an email", () => {
    expect(waitlistEvent({ email: "not-an-email", source: "landing" }, "phc_x")).toBeNull();
  });

  it("drops a submission that filled the hidden field", () => {
    expect(
      waitlistEvent({ email: "bot@example.com", source: "landing", company: "Acme" }, "phc_x"),
    ).toBeNull();
  });
});

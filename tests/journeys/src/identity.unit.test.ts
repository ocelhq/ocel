import { describe, expect, it } from "bun:test";
import { appHostname, projectSlug, runIdentity } from "./identity";

describe("run identity", () => {
  it("is the GitHub run id in CI", () => {
    expect(runIdentity({ GITHUB_RUN_ID: "1849302" }, "runner")).toBe("1849302");
  });

  it("is the username prefixed with local everywhere else", () => {
    expect(runIdentity({}, "ada")).toBe("local-ada");
  });

  it("ignores an empty GitHub run id", () => {
    expect(runIdentity({ GITHUB_RUN_ID: "" }, "ada")).toBe("local-ada");
  });
});

describe("project slug", () => {
  it("carries the run id behind the sweep prefix", () => {
    expect(projectSlug("express", "local-ada")).toBe("j-local-ada-express");
  });
});

describe("app hostname", () => {
  it("hangs app and slug off the zone as one label", () => {
    expect(appHostname("web", "j-local-ada-express", "journey.test")).toBe(
      "web-j-local-ada-express.journey.test",
    );
  });

  it("is absent without a zone", () => {
    expect(appHostname("web", "express", undefined)).toBeUndefined();
  });

  it("refuses a label a dns label cannot hold", () => {
    expect(() => appHostname("web", `j-1-${"e".repeat(60)}`, "journey.test")).toThrow(/63/);
  });
});

import { describe, expect, it } from "vitest";
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
  it("is app, slug and zone", () => {
    expect(appHostname("web", "j-local-ada-express", "journey.test")).toBe(
      "web.j-local-ada-express.journey.test",
    );
  });

  it("is absent without a zone", () => {
    expect(appHostname("web", "express", undefined)).toBeUndefined();
  });
});

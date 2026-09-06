import { describe, expect, it } from "bun:test";
import { appHostname, isStranded, projectSlug, runIdentity } from "./identity";

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
    expect(projectSlug("node", "local-ada")).toBe("j-local-ada-node");
  });
});

describe("isStranded", () => {
  const slugs = ["ocelhq", "j-local-ada-node", "j-42-node", "j-4242-node"];

  it("leaves projects that are not the harness's alone", () => {
    expect(slugs.filter((slug) => isStranded(slug, "42"))).not.toContain("ocelhq");
  });

  it("leaves this run's own projects standing", () => {
    expect(slugs.filter((slug) => isStranded(slug, "42"))).toEqual([
      "j-local-ada-node",
      "j-4242-node",
    ]);
  });

  it("takes every harness project when the run identity matches none", () => {
    expect(slugs.filter((slug) => isStranded(slug, "local-bob"))).toEqual([
      "j-local-ada-node",
      "j-42-node",
      "j-4242-node",
    ]);
  });
});

describe("app hostname", () => {
  it("hangs app and slug off the zone as one label", () => {
    expect(appHostname("web", "j-local-ada-node", "journey.test")).toBe(
      "web-j-local-ada-node.journey.test",
    );
  });

  it("is absent without a zone", () => {
    expect(appHostname("web", "node", undefined)).toBeUndefined();
  });

  it("refuses a label a dns label cannot hold", () => {
    expect(() => appHostname("web", `j-1-${"e".repeat(60)}`, "journey.test")).toThrow(/63/);
  });
});

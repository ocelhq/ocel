import { describe, expect, it } from "bun:test";
import { projectSlugs, projectsDestroyArgs } from "./projects";

function listed(projects: unknown[]): string {
  return `${JSON.stringify({ ok: true, data: { projects } })}\n`;
}

describe("projectSlugs", () => {
  it("reads the slug of every project ocel projects lists", () => {
    const stdout = listed([
      {
        slug: "j-1874-deploy-node",
        tiers: ["TIER_PRODUCTION", "TIER_PREVIEW"],
        edge: "cloudfront",
        productionPromotionId: "p-1",
        previews: 1,
        lastDeployedAt: "2026-10-19T10:00:00Z",
      },
      { slug: "j-1874-deploy-next", tiers: ["TIER_PRODUCTION"], previews: 0 },
    ]);

    expect(projectSlugs(stdout)).toEqual(["j-1874-deploy-node", "j-1874-deploy-next"]);
  });

  it("reads an account with no project as no slugs", () => {
    expect(projectSlugs(listed([]))).toEqual([]);
  });

  it("refuses a listing that failed, and says why", () => {
    const stdout = JSON.stringify({
      ok: false,
      error: { code: "not_ready", message: "the production tier is not bootstrapped" },
    });

    expect(() => projectSlugs(stdout)).toThrow(/the production tier is not bootstrapped/);
  });

  it("refuses output that is not the ocel projects result", () => {
    expect(() => projectSlugs(`${JSON.stringify({ ok: true, data: {} })}\n`)).toThrow(
      /ocel projects --json printed no project list/,
    );
    expect(() => projectSlugs("Projects in aws 523862207780")).toThrow(
      /ocel projects --json printed no JSON/,
    );
  });

  it("refuses a listed project that carries no slug", () => {
    expect(() => projectSlugs(listed([{ tiers: ["TIER_PRODUCTION"] }]))).toThrow(
      /ocel projects --json listed a project with no slug/,
    );
  });
});

describe("projectsDestroyArgs", () => {
  it("destroys the project in both tiers when no tier is named", () => {
    expect(projectsDestroyArgs()).toEqual(["projects", "destroy", "--yes"]);
  });

  it("destroys only the named tier, so the other stays", () => {
    expect(projectsDestroyArgs("preview")).toEqual([
      "projects",
      "destroy",
      "--tier",
      "preview",
      "--yes",
    ]);
  });
});

import { describe, expect, it } from "bun:test";
import { fixtures } from "../matrix/fixtures";
import { fixture } from "../matrix/types";
import { defaults, registry } from "../matrix/variants";
import type { StepResult } from "../run/results";
import { registryPackages } from "./packages";

function result(cell: string, title: string, outcome: StepResult["outcome"]): StepResult {
  return { cell, title, outcome, startTime: 0, duration: 1 };
}

describe("registryPackages", () => {
  it("names the one ghcr package every run of the journey pushes into", () => {
    expect(registryPackages(fixtures, "vps")).toEqual([
      { org: "ocelhq", name: "journey-vps/web", deployed: false },
    ]);
  });

  it("names nothing on a target no registry cell runs on", () => {
    expect(registryPackages(fixtures, "aws")).toEqual([]);
  });

  it("names one package per app a registry cell deploys, however many fixtures share it", () => {
    const shared = [
      fixture("deploy/one", { apps: ["web"], checks: [], on: { vps: [registry] } }),
      fixture("deploy/two", {
        apps: ["web", "api"],
        checks: [],
        on: { vps: [defaults, registry] },
      }),
      fixture("deploy/three", { apps: ["worker"], checks: [], on: { vps: [defaults] } }),
    ];
    expect(registryPackages(shared, "vps")).toEqual([
      { org: "ocelhq", name: "journey-vps/web", deployed: false },
      { org: "ocelhq", name: "journey-vps/api", deployed: false },
    ]);
  });

  it("names a package after the app as the CLI sanitizes it into an image repository", () => {
    const shouting = [
      fixture("deploy/one", { apps: ["Api_Server"], checks: [], on: { vps: [registry] } }),
    ];
    expect(registryPackages(shouting, "vps")).toEqual([
      { org: "ocelhq", name: "journey-vps/api-server", deployed: false },
    ]);
  });

  it("marks deployed the package of each app a registry cell passed its deploy step with", () => {
    const pair = [
      fixture("deploy/two", {
        apps: ["web", "api"],
        checks: [],
        on: { vps: [defaults, registry] },
      }),
    ];
    const results = [
      result("deploy/two/web", "deploy", "passed"),
      result("deploy/two/api", "deploy", "passed"),
      result("deploy/two-registry/web", "deploy", "passed"),
      result("deploy/two-registry/api", "deploy", "failed"),
      result("deploy/two-registry/api", "destroy", "passed"),
    ];
    expect(registryPackages(pair, "vps", results)).toEqual([
      { org: "ocelhq", name: "journey-vps/web", deployed: true },
      { org: "ocelhq", name: "journey-vps/api", deployed: false },
    ]);
  });
});

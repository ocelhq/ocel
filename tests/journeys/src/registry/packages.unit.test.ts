import { describe, expect, it } from "bun:test";
import { fixtures } from "../matrix/fixtures";
import { fixture } from "../matrix/types";
import { defaults, registry } from "../matrix/variants";
import { registryPackages } from "./packages";

describe("registryPackages", () => {
  it("names the one ghcr package every run of the journey pushes into", () => {
    expect(registryPackages(fixtures)).toEqual([{ org: "ocelhq", name: "journey-vps/web" }]);
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
    expect(registryPackages(shared)).toEqual([
      { org: "ocelhq", name: "journey-vps/web" },
      { org: "ocelhq", name: "journey-vps/api" },
    ]);
  });

  it("names a package after the app as the CLI sanitizes it into an image repository", () => {
    const shouting = [
      fixture("deploy/one", { apps: ["Api_Server"], checks: [], on: { vps: [registry] } }),
    ];
    expect(registryPackages(shouting)).toEqual([{ org: "ocelhq", name: "journey-vps/api-server" }]);
  });
});

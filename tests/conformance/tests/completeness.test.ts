import { readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { checks } from "../src/checks/registry";
import { examples } from "../src/examples";
import { conformanceCapabilities } from "../src/types";

const here = path.dirname(fileURLToPath(import.meta.url));
const checkModules = readdirSync(path.join(here, "..", "src", "checks"))
  .filter((file) => file.endsWith(".ts") && file !== "registry.ts")
  .map((file) => file.slice(0, -3))
  .sort();

describe("conformance registry", () => {
  it("registers one check module for every conformance capability", () => {
    expect(Object.keys(checks).sort()).toEqual(
      [...conformanceCapabilities].sort(),
    );
    expect(checkModules).toEqual(Object.keys(checks).sort());
  });

  it("has a fixture for every conformance capability and every check", () => {
    const claimed = new Set(examples.flatMap((example) => example.capabilities));
    for (const capability of conformanceCapabilities) {
      expect(claimed.has(capability), `${capability} has no fixture`).toBe(true);
    }
    for (const check of Object.keys(checks)) {
      expect(claimed.has(check as never), `${check} check has no fixture`).toBe(
        true,
      );
    }
  });

  it("runs each external link tool only against AWS", () => {
    const fixtures = examples.filter((example) =>
      example.capabilities.some((capability) => capability === "links"),
    );
    expect(
      fixtures.map((example) => ({
        name: example.name,
        targets: "targets" in example ? example.targets : [],
        tool: "linkTool" in example ? example.linkTool : undefined,
      })),
    ).toEqual([
      { name: "with-sst", targets: ["aws"], tool: "sst" },
      { name: "with-pulumi", targets: ["aws"], tool: "pulumi" },
    ]);
  });
});

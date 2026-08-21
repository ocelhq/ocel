import { readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { checks } from "../src/checks/registry";
import { examples } from "../src/examples";
import { sdkCapabilities } from "../src/types";

const here = path.dirname(fileURLToPath(import.meta.url));
const checkModules = readdirSync(path.join(here, "..", "src", "checks"))
  .filter((file) => file.endsWith(".ts") && file !== "registry.ts")
  .map((file) => file.slice(0, -3))
  .sort();

describe("conformance registry", () => {
  it("registers one check module for every SDK capability", () => {
    expect(Object.keys(checks).sort()).toEqual([...sdkCapabilities].sort());
    expect(checkModules).toEqual(Object.keys(checks).sort());
  });

  it("has a fixture for every SDK capability and every check", () => {
    const claimed = new Set(examples.flatMap((example) => example.capabilities));
    for (const capability of sdkCapabilities) {
      expect(claimed.has(capability), `${capability} has no fixture`).toBe(true);
    }
    for (const check of Object.keys(checks)) {
      expect(claimed.has(check as never), `${check} check has no fixture`).toBe(
        true,
      );
    }
  });
});

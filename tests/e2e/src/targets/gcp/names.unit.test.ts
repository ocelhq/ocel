import { describe, expect, it } from "bun:test";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { repoRoot } from "../../paths";
import {
  APP_LABEL,
  ENVIRONMENT_LABEL,
  labelValue,
  NAMESPACE_LABEL,
  namespaceField,
  namespaceOf,
  PRODUCTION_ENVIRONMENT,
  PRODUCTION_TIER,
  PROJECT_LABEL,
  TIER_LABEL,
} from "./names";

describe("namespaceField", () => {
  it("is a namespace of twelve characters or fewer as it is", () => {
    expect(namespaceField("ocel")).toBe("ocel");
    expect(namespaceField("ocel-nightly")).toBe("ocel-nightly");
  });

  it("compacts a longer one to its head and six hex of its digest, as the provider names it", () => {
    expect(namespaceField("acme-staging-eu-west-blue-team")).toBe("acme-20ddf6");
    expect(namespaceField("j-1874-deploy-next-cloudflare")).toBe("j-187-da21fb");
  });

  it("never leaves a head ending in the word app", () => {
    expect(namespaceField("x-apple-orchard-namespace")).toBe("x-ap-7e71aa");
  });
});

describe("labelValue", () => {
  it("keeps a value that fits a label", () => {
    expect(labelValue("j-1-deploy-node")).toBe("j-1-deploy-node");
  });

  it("cuts a longer one ahead of the hash tag of the whole, as the provider labels it", () => {
    expect(labelValue("a".repeat(70))).toBe(`${"a".repeat(55)}-6bd5e5`);
  });
});

describe("namespaceOf", () => {
  it("is the namespace the environment names, and ocel when it names none", () => {
    expect(namespaceOf({})).toBe("ocel");
    expect(namespaceOf({ OCEL_NAMESPACE: " nightly " })).toBe("nightly");
  });
});

describe("the labels a service is found by", () => {
  it("are the keys the provider labels every service of a project with", async () => {
    const go = await readFile(
      path.join(repoRoot, "platform", "gcp", "provider", "internal", "names", "labels.go"),
      "utf8",
    );
    expect(go).toMatch(new RegExp(`NamespaceLabel\\s*=\\s*"${NAMESPACE_LABEL}"`));
    expect(go).toMatch(new RegExp(`ProjectLabel\\s*=\\s*"${PROJECT_LABEL}"`));
    expect(go).toMatch(new RegExp(`AppLabel\\s*=\\s*"${APP_LABEL}"`));
    expect(go).toMatch(new RegExp(`EnvironmentLabel\\s*=\\s*"${ENVIRONMENT_LABEL}"`));
    expect(go).toMatch(new RegExp(`TierLabel\\s*=\\s*"${TIER_LABEL}"`));
  });

  it("finds the production tier by the name the provider gives it", async () => {
    const go = await readFile(path.join(repoRoot, "pkg", "environment", "tier.go"), "utf8");
    expect(go).toMatch(new RegExp(`TierProduction\\s+Tier\\s*=\\s*"${PRODUCTION_TIER}"`));
  });

  it("finds the production environment by the name the provider gives it", async () => {
    const go = await readFile(
      path.join(repoRoot, "pkg", "stackrecords", "environments.go"),
      "utf8",
    );
    expect(go).toContain(`ProductionEnv = "${PRODUCTION_ENVIRONMENT}"`);
  });
});

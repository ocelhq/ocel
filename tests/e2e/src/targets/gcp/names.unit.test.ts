import { describe, expect, it } from "bun:test";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { repoRoot } from "../../paths";
import {
  APP_LABEL,
  ENVIRONMENT_LABEL,
  fittedSlug,
  NAMESPACE_LABEL,
  namespaceOf,
  PRODUCTION_ENVIRONMENT,
  PROJECT_LABEL,
  roomForSlug,
} from "./names";

describe("roomForSlug", () => {
  it("leaves the slug what a Cloud Run service name has left over", () => {
    expect(roomForSlug("ocel", ["web"])).toBe(22);
  });

  it("counts the longest app, because every app of a fixture shares one slug", () => {
    expect(roomForSlug("ocel", ["next", "express"])).toBe(18);
  });

  it("shrinks with the namespace every name starts with", () => {
    expect(roomForSlug("ocel-nightly", ["web"])).toBe(14);
  });
});

describe("fittedSlug", () => {
  it("leaves a slug that fits the room alone", () => {
    expect(fittedSlug("j-17-deploy-node", 22)).toBe("j-17-deploy-node");
  });

  it("cuts a longer one down to a head and a digest of the whole", () => {
    const fitted = fittedSlug("j-17-deploy-node-container", 20);
    expect(fitted).toHaveLength(20);
    expect(fitted.startsWith("j-17-deploy-n")).toBe(true);
    expect(fitted).not.toBe(fittedSlug("j-17-deploy-node-cloudflare", 20));
  });

  it("never ends the head in a dash", () => {
    const fitted = fittedSlug("j-17-deploy-workspace", 19);
    expect(fitted.startsWith("j-17-deploy-")).toBe(true);
    expect(fitted).toHaveLength(18);
  });

  it("refuses a room too small to tell two cells apart", () => {
    expect(() => fittedSlug("j-17-deploy-node", 7)).toThrow(/room/);
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
      path.join(repoRoot, "platform", "gcp", "provider", "labels.go"),
      "utf8",
    );
    expect(go).toMatch(new RegExp(`namespaceLabel\\s*=\\s*"${NAMESPACE_LABEL}"`));
    expect(go).toMatch(new RegExp(`projectLabel\\s*=\\s*"${PROJECT_LABEL}"`));
    expect(go).toMatch(new RegExp(`appLabel\\s*=\\s*"${APP_LABEL}"`));
    expect(go).toMatch(new RegExp(`environmentLabel\\s*=\\s*"${ENVIRONMENT_LABEL}"`));
  });

  it("finds the production environment by the name the provider gives it", async () => {
    const go = await readFile(
      path.join(repoRoot, "pkg", "stackrecords", "environments.go"),
      "utf8",
    );
    expect(go).toContain(`ProductionEnv = "${PRODUCTION_ENVIRONMENT}"`);
  });
});

import { describe, expect, it } from "bun:test";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { repoRoot } from "../../paths";
import {
  fittedSlug,
  NAMESPACE_LABEL,
  namespaceOf,
  PROJECT_LABEL,
  roomForSlug,
  serviceNames,
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

describe("serviceNames", () => {
  it("are the names the provider gives the app's own service and its index function", () => {
    expect(serviceNames("ocel", "j-37323939460-d-1ab282", "web")).toEqual([
      expect.stringMatching(/^ocel-j-37323939460-d-prod-web-[0-9a-f]{6}$/),
      "ocel-j-37323939-prod-web-index-00714e",
    ]);
  });

  it("sanitize the app as the provider does, so an app whose name is not a slug still finds its services", () => {
    expect(serviceNames("ocel", "j-1-deploy-node", "My App")).toEqual([
      "ocel-j-1-deploy-no-prod-my-app-a3d786",
      "ocel-j-1-dep-prod-my-app-index-99f89b",
    ]);
  });

  it("fit the 37 characters a service a release tags keeps, cutting the project before the app", () => {
    for (const name of serviceNames(
      "ocel-nightly",
      "j-37323939460-deploy-node-a1b2c3",
      "express",
    )) {
      expect(name.length).toBeLessThanOrEqual(37);
      expect(name).toContain("-express-");
    }
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
  });
});

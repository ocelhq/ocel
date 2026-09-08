import { describe, expect, it } from "bun:test";
import { fittedSlug, namespaceOf, roomForSlug, serviceLead } from "./names";

describe("roomForSlug", () => {
  it("leaves the slug what a Cloud Run service name has left over", () => {
    expect(roomForSlug("ocel", ["web"])).toBe(22);
  });

  it("counts the longest app, because every app of a fixture shares one slug", () => {
    expect(roomForSlug("ocel", ["next", "express"])).toBe(18);
  });

  it("shrinks with the namespace every name carries", () => {
    expect(roomForSlug("ocel-nightly", ["web"])).toBe(14);
  });
});

describe("fittedSlug", () => {
  it("leaves a slug the room holds alone", () => {
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

describe("serviceLead", () => {
  it("is what every service of an app is named after, sanitised as the provider sanitises it", () => {
    expect(serviceLead("ocel", "j-17-deploy-node", "web")).toBe("ocel-j-17-deploy-node-prod-web");
  });
});

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { unprovisioned, unprovisionedPhase } from "./unprovisioned.js";

describe("unprovisionedPhase()", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv("NEXT_PHASE", "");
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("is discovery while the declarations are read", () => {
    vi.stubEnv("OCEL_PHASE", "discovery");

    expect(unprovisionedPhase()).toBe("discovery");
  });

  it("is build while next build imports the app's modules", () => {
    vi.stubEnv("NEXT_PHASE", "phase-production-build");

    expect(unprovisionedPhase()).toBe("build");
  });

  it("is unset while next serves the app", () => {
    vi.stubEnv("NEXT_PHASE", "phase-production-server");

    expect(unprovisionedPhase()).toBeUndefined();
  });

  it("is unset once resources are provisioned", () => {
    expect(unprovisionedPhase()).toBeUndefined();
  });
});

describe("unprovisioned()", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("tells a build that a page or route reading the resource must be dynamic", () => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv("NEXT_PHASE", "phase-production-build");

    expect(unprovisioned('postgres("main")', "query").message).toBe(
      "'postgres(\"main\")' cannot be used while the app is being built: tried to access 'query', and resources are provisioned after the build; a page or route that reads it at build time must be dynamic",
    );
  });
});

import { describe, expect, it } from "bun:test";
import { hasReleaseCycle, laneWorkers, targetNamed } from "./index";

const target = { workers: 3 };

describe("laneWorkers", () => {
  it("takes the target's workers when nothing overrides it", () => {
    expect(laneWorkers(target, {})).toBe(3);
  });

  it("takes the override when it names a positive integer", () => {
    expect(laneWorkers(target, { OCEL_JOURNEY_WORKERS: "6" })).toBe(6);
  });

  it("ignores an override that is not a positive integer", () => {
    for (const asked of ["", " ", "0", "-2", "1.5", "many"]) {
      expect(laneWorkers(target, { OCEL_JOURNEY_WORKERS: asked })).toBe(3);
    }
  });
});

describe("targetNamed", () => {
  it("names every target and refuses one it does not run", () => {
    for (const name of ["aws", "dev", "gcp", "vps"]) {
      expect(targetNamed(name).name).toBe(name as "aws" | "dev" | "gcp" | "vps");
    }
    expect(() => targetNamed("azure")).toThrow(
      /no journey target named azure \(aws, dev, gcp, vps\)/,
    );
  });
});

describe("a target's release cycle", () => {
  it("redeploys and rolls back on the targets that keep releases, and nowhere else", () => {
    const cycled = ["aws", "dev", "gcp", "vps"].filter((name) =>
      hasReleaseCycle(targetNamed(name)),
    );
    expect(cycled).toEqual(["aws", "gcp", "vps"]);
  });
});

describe("a target's state", () => {
  it("belongs to the one process that named it", () => {
    expect(targetNamed("vps")).not.toBe(targetNamed("vps"));
  });
});

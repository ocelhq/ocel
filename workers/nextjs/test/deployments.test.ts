import { describe, expect, it } from "vitest";

import {
  resolveDeployment,
  type DeploymentRecord,
  type DeploymentsBinding,
  type DeploymentsDeps,
} from "../src/deployments";

function makeRecord(over: Partial<DeploymentRecord> = {}): DeploymentRecord {
  return {
    app: "web",
    buildId: "build-1",
    routingManifest: { pathnames: [] },
    functionUrls: { "/": "https://fn.example.com" },
    assetPrefix: "build-1",
    isrPrefix: "prod/p1/web/build-1",
    createdAt: 1_000,
    ...over,
  };
}

// A binding stub whose activeBuildId/record calls are counted and can be
// switched between answering and throwing (simulating a store outage), with
// each per-app/build answer configurable independently.
function countingBinding(opts: {
  activeBuildId: Record<string, string | undefined>;
  records: Record<string, DeploymentRecord>;
}): DeploymentsBinding & {
  activeBuildIdCalls: number;
  recordCalls: number;
  down: boolean;
} {
  return {
    activeBuildIdCalls: 0,
    recordCalls: 0,
    down: false,
    async activeBuildId(app: string) {
      this.activeBuildIdCalls++;
      if (this.down) throw new Error("store unreachable");
      return opts.activeBuildId[app];
    },
    async record(app: string, buildId: string) {
      this.recordCalls++;
      if (this.down) throw new Error("store unreachable");
      return opts.records[`${app}/${buildId}`];
    },
  };
}

function deps(
  binding: DeploymentsBinding,
  clock: { ms: number },
  app = "web",
): DeploymentsDeps {
  return { binding, app, now: () => clock.ms };
}

describe("resolveDeployment", () => {
  it("resolves and returns the active Deployment record", async () => {
    const binding = countingBinding({
      activeBuildId: { web: "build-1" },
      records: { "web/build-1": makeRecord() },
    });
    const clock = { ms: 0 };

    const resolution = await resolveDeployment(deps(binding, clock));

    expect(resolution).toEqual({ kind: "found", record: makeRecord() });
  });

  it("returns not-found when no active pointer exists for the app", async () => {
    const binding = countingBinding({ activeBuildId: {}, records: {} });
    const clock = { ms: 0 };

    const resolution = await resolveDeployment(deps(binding, clock));

    expect(resolution).toEqual({ kind: "not-found" });
    expect(binding.recordCalls).toBe(0);
  });

  it("reuses the cached record across requests without re-reading it", async () => {
    const binding = countingBinding({
      activeBuildId: { web: "build-1" },
      records: { "web/build-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const d = deps(binding, clock);

    await resolveDeployment(d);
    await resolveDeployment(d);

    // The pointer is still within its TTL on the second call, so neither RPC
    // fires again.
    expect(binding.activeBuildIdCalls).toBe(1);
    expect(binding.recordCalls).toBe(1);
  });

  it("re-reads the pointer only after its TTL elapses", async () => {
    const binding = countingBinding({
      activeBuildId: { web: "build-1" },
      records: { "web/build-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const d = deps(binding, clock);

    await resolveDeployment(d);
    clock.ms = 4_000; // still within the 5s TTL
    await resolveDeployment(d);
    expect(binding.activeBuildIdCalls).toBe(1);

    clock.ms = 5_001; // TTL elapsed
    const resolution = await resolveDeployment(d);
    expect(binding.activeBuildIdCalls).toBe(2);
    // The record itself is unchanged (same build id), so it's still cached.
    expect(binding.recordCalls).toBe(1);
    expect(resolution).toEqual({ kind: "found", record: makeRecord() });
  });

  it("re-reads the record when the pointer moves to a new build (promotion/rollback)", async () => {
    const activeBuildId: Record<string, string> = { web: "build-1" };
    const binding = countingBinding({
      activeBuildId,
      records: {
        "web/build-1": makeRecord(),
        "web/build-2": makeRecord({ buildId: "build-2" }),
      },
    });
    const clock = { ms: 0 };
    const d = deps(binding, clock);

    const first = await resolveDeployment(d);
    expect(first).toEqual({ kind: "found", record: makeRecord() });

    // A rollback/promotion re-points the app at build-2; the pointer TTL has
    // to elapse before this worker notices.
    activeBuildId.web = "build-2";
    clock.ms = 5_001;
    const second = await resolveDeployment(d);

    expect(second).toEqual({
      kind: "found",
      record: makeRecord({ buildId: "build-2" }),
    });
    expect(binding.recordCalls).toBe(2);
  });

  it("serves the cached record during a transient store outage", async () => {
    const binding = countingBinding({
      activeBuildId: { web: "build-1" },
      records: { "web/build-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const d = deps(binding, clock);

    await resolveDeployment(d); // warms the pointer + record caches

    clock.ms = 5_001; // TTL elapsed, so the next call re-reads the pointer
    binding.down = true;
    const resolution = await resolveDeployment(d);

    expect(resolution).toEqual({ kind: "found", record: makeRecord() });
  });

  it("returns unavailable on a cold isolate when the store is unreachable", async () => {
    const binding = countingBinding({ activeBuildId: {}, records: {} });
    binding.down = true;
    const clock = { ms: 0 };

    const resolution = await resolveDeployment(deps(binding, clock));

    expect(resolution).toEqual({ kind: "unavailable" });
  });

  it("returns unavailable when the record read fails even though the pointer resolved", async () => {
    const binding = countingBinding({
      activeBuildId: { web: "build-1" },
      records: {},
    });
    const clock = { ms: 0 };

    const resolution = await resolveDeployment(deps(binding, clock));

    expect(resolution).toEqual({ kind: "unavailable" });
  });

  it("keeps caches independent across apps", async () => {
    const binding = countingBinding({
      activeBuildId: { web: "build-1", admin: "build-9" },
      records: {
        "web/build-1": makeRecord(),
        "admin/build-9": makeRecord({ app: "admin", buildId: "build-9" }),
      },
    });
    const clock = { ms: 0 };

    const web = await resolveDeployment(deps(binding, clock, "web"));
    const admin = await resolveDeployment(deps(binding, clock, "admin"));

    expect(web).toEqual({ kind: "found", record: makeRecord() });
    expect(admin).toEqual({
      kind: "found",
      record: makeRecord({ app: "admin", buildId: "build-9" }),
    });
  });

  it("evicts the oldest record once the bounded LRU is exceeded", async () => {
    const records: Record<string, DeploymentRecord> = {};
    const activeBuildId: Record<string, string> = {};
    for (let i = 0; i < 20; i++) {
      const buildId = `build-${i}`;
      records[`web/${buildId}`] = makeRecord({ buildId });
    }
    const binding = countingBinding({ activeBuildId, records });
    const clock = { ms: 0 };

    // Walk through 20 distinct build ids for the same app, each one a fresh
    // pointer read (TTL bumped past every time) so every record is fetched
    // and memoized in turn.
    for (let i = 0; i < 20; i++) {
      activeBuildId.web = `build-${i}`;
      clock.ms = (i + 1) * 6_000;
      const resolution = await resolveDeployment(deps(binding, clock));
      expect(resolution).toEqual({
        kind: "found",
        record: makeRecord({ buildId: `build-${i}` }),
      });
    }

    // The oldest build (build-0) fell out of the 16-entry bound: resolving it
    // again costs a fresh record RPC rather than serving from memory.
    const callsBefore = binding.recordCalls;
    activeBuildId.web = "build-0";
    clock.ms = 21 * 6_000;
    await resolveDeployment(deps(binding, clock));
    expect(binding.recordCalls).toBe(callsBefore + 1);

    // The most recently used build (build-19) is still warm.
    const callsBefore2 = binding.recordCalls;
    activeBuildId.web = "build-19";
    clock.ms = 22 * 6_000;
    await resolveDeployment(deps(binding, clock));
    expect(binding.recordCalls).toBe(callsBefore2);
  });
});

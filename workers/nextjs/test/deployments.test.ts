import { describe, expect, it } from "vitest";

import {
  resolveDeployment,
  type ActiveRecordResult,
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

// A binding stub whose activeRecord calls are counted and can be switched to
// throwing (simulating a store outage). It resolves each app's active build id
// and records from injected maps, and honours knownBuildId the same way the
// real store does — omitting the record when the caller's build is still live.
function countingBinding(opts: {
  activeBuildId: Record<string, string | undefined>;
  records: Record<string, DeploymentRecord>;
}): DeploymentsBinding & {
  activeRecordCalls: number;
  // Whether the last answered call carried a record (vs. an "unchanged" echo).
  lastCarriedRecord: boolean;
  down: boolean;
} {
  return {
    activeRecordCalls: 0,
    lastCarriedRecord: false,
    down: false,
    async activeRecord(
      _slug: string,
      app: string,
      knownBuildId?: string,
    ): Promise<ActiveRecordResult> {
      this.activeRecordCalls++;
      if (this.down) throw new Error("store unreachable");
      const buildId = opts.activeBuildId[app];
      if (!buildId) {
        this.lastCarriedRecord = false;
        return { kind: "no-pointer" };
      }
      if (buildId === knownBuildId) {
        this.lastCarriedRecord = false;
        return { kind: "unchanged", buildId };
      }
      const record = opts.records[`${app}/${buildId}`];
      if (!record) {
        this.lastCarriedRecord = false;
        return { kind: "dangling", buildId };
      }
      this.lastCarriedRecord = true;
      return { kind: "record", buildId, record };
    },
  };
}

function deps(
  binding: DeploymentsBinding,
  clock: { ms: number },
  app = "web",
): DeploymentsDeps {
  return { binding, slug: "acme-web", app, now: () => clock.ms };
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
  });

  it("serves the cached record within the TTL without calling the store", async () => {
    const binding = countingBinding({
      activeBuildId: { web: "build-1" },
      records: { "web/build-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const d = deps(binding, clock);

    await resolveDeployment(d);
    await resolveDeployment(d);

    // The record is still within its TTL on the second call, so no RPC fires.
    expect(binding.activeRecordCalls).toBe(1);
  });

  it("revalidates after the TTL without re-transferring an unchanged record", async () => {
    const binding = countingBinding({
      activeBuildId: { web: "build-1" },
      records: { "web/build-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const d = deps(binding, clock);

    await resolveDeployment(d);
    clock.ms = 4_000; // still within the 5s TTL
    await resolveDeployment(d);
    expect(binding.activeRecordCalls).toBe(1);

    clock.ms = 5_001; // TTL elapsed
    const resolution = await resolveDeployment(d);
    expect(binding.activeRecordCalls).toBe(2);
    // The build is unchanged, so the store echoed it back without the record;
    // the cached record still stands.
    expect(binding.lastCarriedRecord).toBe(false);
    expect(resolution).toEqual({ kind: "found", record: makeRecord() });
  });

  it("re-reads the record when the build moves (promotion/rollback)", async () => {
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

    // A rollback/promotion re-points the app at build-2; the TTL has to elapse
    // before this worker notices.
    activeBuildId.web = "build-2";
    clock.ms = 5_001;
    const second = await resolveDeployment(d);

    expect(second).toEqual({
      kind: "found",
      record: makeRecord({ buildId: "build-2" }),
    });
    expect(binding.lastCarriedRecord).toBe(true);
  });

  it("serves the cached record during a transient store outage", async () => {
    const binding = countingBinding({
      activeBuildId: { web: "build-1" },
      records: { "web/build-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const d = deps(binding, clock);

    await resolveDeployment(d); // warms the record cache

    clock.ms = 5_001; // TTL elapsed, so the next call revalidates
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

  it("returns unavailable when the pointer names a build with no record", async () => {
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
});

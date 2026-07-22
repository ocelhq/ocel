import { describe, expect, it } from "vitest";

import {
  resolveDeployment,
  type PointerRecordResult,
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

// A binding stub whose pointerRecord calls are counted and can be switched to
// throwing (simulating a store outage). It resolves each pointer's build id and
// records from injected maps, and honours knownBuildId the same way the real
// store does — omitting the record when the caller's build is still live. Build
// ids are keyed by "<app>/<pointer>" so a preview pointer resolves independently
// of the default one; an absent pointer arg keys as the empty string.
function countingBinding(opts: {
  pointerBuildId: Record<string, string | undefined>;
  records: Record<string, DeploymentRecord>;
}): DeploymentsBinding & {
  pointerRecordCalls: number;
  // Whether the last answered call carried a record (vs. an "unchanged" echo).
  lastCarriedRecord: boolean;
  down: boolean;
} {
  return {
    pointerRecordCalls: 0,
    lastCarriedRecord: false,
    down: false,
    async pointerRecord(args: {
      slug: string;
      app: string;
      pointer?: string;
      knownBuildId?: string;
    }): Promise<PointerRecordResult> {
      this.pointerRecordCalls++;
      if (this.down) throw new Error("store unreachable");
      const buildId = opts.pointerBuildId[`${args.app}/${args.pointer ?? ""}`];
      if (!buildId) {
        this.lastCarriedRecord = false;
        return { kind: "no-pointer" };
      }
      if (buildId === args.knownBuildId) {
        this.lastCarriedRecord = false;
        return { kind: "unchanged", buildId };
      }
      const record = opts.records[`${args.app}/${buildId}`];
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
      pointerBuildId: { "web/": "build-1" },
      records: { "web/build-1": makeRecord() },
    });
    const clock = { ms: 0 };

    const resolution = await resolveDeployment(deps(binding, clock));

    expect(resolution).toEqual({ kind: "found", record: makeRecord() });
  });

  it("returns not-found when no active pointer exists for the app", async () => {
    const binding = countingBinding({ pointerBuildId: {}, records: {} });
    const clock = { ms: 0 };

    const resolution = await resolveDeployment(deps(binding, clock));

    expect(resolution).toEqual({ kind: "not-found" });
  });

  it("serves the cached record within the TTL without calling the store", async () => {
    const binding = countingBinding({
      pointerBuildId: { "web/": "build-1" },
      records: { "web/build-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const d = deps(binding, clock);

    await resolveDeployment(d);
    await resolveDeployment(d);

    // The record is still within its TTL on the second call, so no RPC fires.
    expect(binding.pointerRecordCalls).toBe(1);
  });

  it("revalidates after the TTL without re-transferring an unchanged record", async () => {
    const binding = countingBinding({
      pointerBuildId: { "web/": "build-1" },
      records: { "web/build-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const d = deps(binding, clock);

    await resolveDeployment(d);
    clock.ms = 4_000; // still within the 5s TTL
    await resolveDeployment(d);
    expect(binding.pointerRecordCalls).toBe(1);

    clock.ms = 5_001; // TTL elapsed
    const resolution = await resolveDeployment(d);
    expect(binding.pointerRecordCalls).toBe(2);
    // The build is unchanged, so the store echoed it back without the record;
    // the cached record still stands.
    expect(binding.lastCarriedRecord).toBe(false);
    expect(resolution).toEqual({ kind: "found", record: makeRecord() });
  });

  it("re-reads the record when the build moves (promotion/rollback)", async () => {
    const pointerBuildId: Record<string, string> = { "web/": "build-1" };
    const binding = countingBinding({
      pointerBuildId,
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
    pointerBuildId["web/"] = "build-2";
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
      pointerBuildId: { "web/": "build-1" },
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
    const binding = countingBinding({ pointerBuildId: {}, records: {} });
    binding.down = true;
    const clock = { ms: 0 };

    const resolution = await resolveDeployment(deps(binding, clock));

    expect(resolution).toEqual({ kind: "unavailable" });
  });

  it("returns unavailable when the pointer names a build with no record", async () => {
    const binding = countingBinding({
      pointerBuildId: { "web/": "build-1" },
      records: {},
    });
    const clock = { ms: 0 };

    const resolution = await resolveDeployment(deps(binding, clock));

    expect(resolution).toEqual({ kind: "unavailable" });
  });

  it("keeps caches independent across apps", async () => {
    const binding = countingBinding({
      pointerBuildId: { "web/": "build-1", "admin/": "build-9" },
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

  it("resolves a named preview pointer independently of the default", async () => {
    const previewRecord = makeRecord({ buildId: "preview-build" });
    const binding = countingBinding({
      pointerBuildId: {
        "web/": "build-1",
        "web/flaky-web-2626": "preview-build",
      },
      records: {
        "web/build-1": makeRecord(),
        "web/preview-build": previewRecord,
      },
    });
    const clock = { ms: 0 };

    const production = await resolveDeployment(deps(binding, clock));
    const preview = await resolveDeployment({
      binding,
      slug: "acme-web",
      app: "web",
      pointer: "flaky-web-2626",
      now: () => clock.ms,
    });

    expect(production).toEqual({ kind: "found", record: makeRecord() });
    expect(preview).toEqual({ kind: "found", record: previewRecord });
    // Two distinct pointers, so two distinct store round trips (no cross-serve).
    expect(binding.pointerRecordCalls).toBe(2);
  });
});

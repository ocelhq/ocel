import { describe, expect, it } from "vitest";

import {
  RECORD_CACHE_MAX,
  resolveDeployment,
  type PointerRecordResult,
  type DeploymentRecord,
  type DeploymentsBinding,
  type DeploymentsDeps,
} from "../src/deployments";

function makeRecord(over: Partial<DeploymentRecord> = {}): DeploymentRecord {
  return {
    app: "web",
    framework: "next",
    buildId: "build-1",
    routingManifest: { pathnames: [] },
    functionUrls: { "/": "https://fn.example.com" },
    assetPrefix: "build-1",
    isrPrefix: "prod/p1/web/build-1",
    createdAt: 1_000,
    ...over,
  };
}

function countingBinding(opts: {
  pointerBuildId: Record<string, string | undefined>;
  records: Record<string, DeploymentRecord>;
}): DeploymentsBinding & {
  pointerRecordCalls: number;
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
  host = `${app}.acme.com`,
): DeploymentsDeps {
  return { binding, slug: "acme-web", host, app, now: () => clock.ms };
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
      host: "flaky-web-2626.acme.com",
      app: "web",
      pointer: "flaky-web-2626",
      now: () => clock.ms,
    });

    expect(production).toEqual({ kind: "found", record: makeRecord() });
    expect(preview).toEqual({ kind: "found", record: previewRecord });
    expect(binding.pointerRecordCalls).toBe(2);
  });

  it("keeps caches independent across projects sharing one binding", async () => {
    const records: Record<string, DeploymentRecord> = {
      acme: makeRecord({ isrPrefix: "prev/acme/web/build-1" }),
      globex: makeRecord({ isrPrefix: "prev/globex/web/build-1" }),
    };
    const binding: DeploymentsBinding = {
      async pointerRecord(args) {
        const record = records[args.slug];
        if (!record) return { kind: "no-pointer" };
        return { kind: "record", buildId: record.buildId, record };
      },
    };
    const clock = { ms: 0 };

    const acme = await resolveDeployment({
      binding,
      slug: "acme",
      host: "acme--pr-42.preview.ocel.sh",
      app: "web",
      pointer: "pr-42",
      now: () => clock.ms,
    });
    const globex = await resolveDeployment({
      binding,
      slug: "globex",
      host: "globex--pr-42.preview.ocel.sh",
      app: "web",
      pointer: "pr-42",
      now: () => clock.ms,
    });

    expect(acme).toEqual({ kind: "found", record: records.acme });
    expect(globex).toEqual({ kind: "found", record: records.globex });
  });

  it("caches on the host, so one host reuses the entry within the TTL", async () => {
    let calls = 0;
    const binding: DeploymentsBinding = {
      async pointerRecord() {
        calls++;
        return { kind: "record", buildId: "build-1", record: makeRecord() };
      },
    };
    const clock = { ms: 0 };
    const d: DeploymentsDeps = {
      binding,
      slug: "acme",
      host: "acme--pr-42.preview.ocel.sh",
      pointer: "pr-42",
      now: () => clock.ms,
    };

    await resolveDeployment(d);
    await resolveDeployment(d);

    expect(calls).toBe(1);
  });

  it("evicts the oldest host once the cache is full", async () => {
    const calls: Record<string, number> = {};
    const binding: DeploymentsBinding = {
      async pointerRecord(args) {
        calls[args.slug] = (calls[args.slug] ?? 0) + 1;
        return { kind: "record", buildId: args.slug, record: makeRecord() };
      },
    };
    const clock = { ms: 0 };
    const resolve = (n: number) =>
      resolveDeployment({
        binding,
        slug: `p${n}`,
        host: `p${n}.preview.ocel.sh`,
        pointer: "pr-1",
        now: () => clock.ms,
      });

    for (let n = 0; n <= RECORD_CACHE_MAX; n++) await resolve(n);

    await resolve(1);
    await resolve(0);

    expect(calls.p1).toBe(1);
    expect(calls.p0).toBe(2);
  });

  it("evicts least-recently-used, so a re-read host outlives an older one", async () => {
    const calls: Record<string, number> = {};
    const binding: DeploymentsBinding = {
      async pointerRecord(args) {
        calls[args.slug] = (calls[args.slug] ?? 0) + 1;
        return { kind: "record", buildId: args.slug, record: makeRecord() };
      },
    };
    const clock = { ms: 0 };
    const resolve = (n: number) =>
      resolveDeployment({
        binding,
        slug: `q${n}`,
        host: `q${n}.preview.ocel.sh`,
        pointer: "pr-1",
        now: () => clock.ms,
      });

    for (let n = 0; n < RECORD_CACHE_MAX; n++) await resolve(n);

    await resolve(0);
    expect(calls.q0).toBe(1);

    await resolve(RECORD_CACHE_MAX);

    await resolve(0);
    await resolve(1);

    expect(calls.q0).toBe(1);
    expect(calls.q1).toBe(2);
  });

  it("passes an absent app through and maps ambiguous-app to not-found", async () => {
    let seen: { app?: string } | undefined;
    const binding: DeploymentsBinding = {
      async pointerRecord(args) {
        seen = args;
        return { kind: "ambiguous-app" };
      },
    };
    const clock = { ms: 0 };

    const resolution = await resolveDeployment({
      binding,
      slug: "acme",
      host: "acme--pr-42.preview.ocel.sh",
      pointer: "pr-42",
      now: () => clock.ms,
    });

    expect(seen?.app).toBeUndefined();
    expect(resolution).toEqual({ kind: "not-found" });
  });
});

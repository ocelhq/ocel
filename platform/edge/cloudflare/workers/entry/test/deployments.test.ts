import { afterEach, describe, expect, it, vi } from "vitest";

import {
  type DeploymentRecord,
  type DeploymentsBinding,
  type DeploymentsDeps,
  type PointerRecordResult,
  RECORD_CACHE_MAX,
  resolveDeployment,
} from "../src/deployments";
import { answerEveryRecordWith } from "./origin-deps";

function makeRecord(over: Partial<DeploymentRecord> = {}): DeploymentRecord {
  return {
    app: "web",
    framework: "next",
    identity: "deploy-1",
    deploymentId: "deploy-1",
    buildId: "build-1",
    routingManifest: { pathnames: [] },
    functionUrls: { "/": "https://fn.example.com" },
    assetPrefix: "deploy-1",
    isrPrefix: "prod/p1/web/build-1",
    createdAt: 1_000,
    ...over,
  };
}

type CountingBinding = DeploymentsBinding & {
  pointerRecordCalls: number;
  labelRecordCalls: number;
  lastReturnedRecord: boolean;
  down: boolean;
};

function createCountingBinding(opts: {
  pointerIdentity: Record<string, string | undefined>;
  records: Record<string, DeploymentRecord>;
}): CountingBinding {
  const answer = (
    binding: CountingBinding,
    key: string,
    app: string,
    knownIdentity?: string,
  ): PointerRecordResult => {
    if (binding.down) throw new Error("store unreachable");
    const identity = opts.pointerIdentity[key];
    binding.lastReturnedRecord = false;
    if (!identity) return { kind: "no-pointer" };
    if (identity === knownIdentity) return { kind: "unchanged", identity };
    const record = opts.records[`${app}/${identity}`];
    if (!record) return { kind: "dangling", identity };
    binding.lastReturnedRecord = true;
    return { kind: "record", identity, record };
  };
  const binding: CountingBinding = {
    pointerRecordCalls: 0,
    labelRecordCalls: 0,
    lastReturnedRecord: false,
    down: false,
    async readPointerRecord(args) {
      binding.pointerRecordCalls++;
      return answer(binding, `${args.app}/`, args.app ?? "", args.knownIdentity);
    },
    async readLabelRecord(args) {
      binding.labelRecordCalls++;
      return answer(binding, args.label, "web", args.knownIdentity);
    },
  };
  return binding;
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
    const binding = createCountingBinding({
      pointerIdentity: { "web/": "deploy-1" },
      records: { "web/deploy-1": makeRecord() },
    });
    const clock = { ms: 0 };

    const resolution = await resolveDeployment(deps(binding, clock));

    expect(resolution).toEqual({ kind: "found", record: makeRecord() });
  });

  it("returns not-found when no active pointer exists for the app", async () => {
    const binding = createCountingBinding({ pointerIdentity: {}, records: {} });
    const clock = { ms: 0 };

    const resolution = await resolveDeployment(deps(binding, clock));

    expect(resolution).toEqual({ kind: "not-found" });
  });

  it("reports unavailable when the store says unchanged but nothing is cached", async () => {
    const binding = answerEveryRecordWith(async () => {
      return { kind: "unchanged", identity: "deploy-1" };
    });

    expect(await resolveDeployment(deps(binding, { ms: 0 }))).toEqual({ kind: "unavailable" });
  });

  it("serves the cached record within the TTL without calling the store", async () => {
    const binding = createCountingBinding({
      pointerIdentity: { "web/": "deploy-1" },
      records: { "web/deploy-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const d = deps(binding, clock);

    await resolveDeployment(d);
    await resolveDeployment(d);

    expect(binding.pointerRecordCalls).toBe(1);
  });

  it("revalidates after the TTL without re-transferring an unchanged record", async () => {
    const binding = createCountingBinding({
      pointerIdentity: { "web/": "deploy-1" },
      records: { "web/deploy-1": makeRecord() },
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
    expect(binding.lastReturnedRecord).toBe(false);
    expect(resolution).toEqual({ kind: "found", record: makeRecord() });
  });

  it("re-reads the record when the build moves (promotion/rollback)", async () => {
    const pointerIdentity: Record<string, string> = { "web/": "deploy-1" };
    const binding = createCountingBinding({
      pointerIdentity,
      records: {
        "web/deploy-1": makeRecord(),
        "web/deploy-2": makeRecord({ identity: "deploy-2" }),
      },
    });
    const clock = { ms: 0 };
    const d = deps(binding, clock);

    const first = await resolveDeployment(d);
    expect(first).toEqual({ kind: "found", record: makeRecord() });

    pointerIdentity["web/"] = "deploy-2";
    clock.ms = 5_001;
    const second = await resolveDeployment(d);

    expect(second).toEqual({
      kind: "found",
      record: makeRecord({ identity: "deploy-2" }),
    });
    expect(binding.lastReturnedRecord).toBe(true);
  });

  it("serves the cached record during a transient store outage", async () => {
    const binding = createCountingBinding({
      pointerIdentity: { "web/": "deploy-1" },
      records: { "web/deploy-1": makeRecord() },
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
    const binding = createCountingBinding({ pointerIdentity: {}, records: {} });
    binding.down = true;
    const clock = { ms: 0 };

    const resolution = await resolveDeployment(deps(binding, clock));

    expect(resolution).toEqual({ kind: "unavailable" });
  });

  afterEach(() => vi.restoreAllMocks());

  it("logs a store read that failed before answering unavailable", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const binding = createCountingBinding({ pointerIdentity: {}, records: {} });
    binding.down = true;
    const clock = { ms: 0 };

    const resolution = await resolveDeployment(deps(binding, clock));

    expect(resolution).toEqual({ kind: "unavailable" });
    expect(errors).toHaveBeenCalledTimes(1);
    expect(errors.mock.calls[0]?.[0]).toContain("acme-web/web");
    expect(errors.mock.calls[0]?.[0]).toContain("answering 503");
    expect(errors.mock.calls[0]?.[1]).toEqual(new Error("store unreachable"));
  });

  it("logs a store read that failed before serving the record it cached", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const binding = createCountingBinding({
      pointerIdentity: { "web/": "deploy-1" },
      records: { "web/deploy-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const d = deps(binding, clock);
    await resolveDeployment(d);

    clock.ms = 5_001;
    binding.down = true;
    const resolution = await resolveDeployment(d);

    expect(resolution).toEqual({ kind: "found", record: makeRecord() });
    expect(errors).toHaveBeenCalledTimes(1);
    expect(errors.mock.calls[0]?.[0]).toContain("acme-web/web");
    expect(errors.mock.calls[0]?.[0]).toContain("cached 5s ago");
  });

  it("logs nothing when the store answers", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const binding = createCountingBinding({
      pointerIdentity: { "web/": "deploy-1" },
      records: { "web/deploy-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const d = deps(binding, clock);
    await resolveDeployment(d);
    clock.ms = 5_001;
    await resolveDeployment(d);

    expect(errors).not.toHaveBeenCalled();
  });

  it("returns unavailable when the pointer names a build with no record", async () => {
    const binding = createCountingBinding({
      pointerIdentity: { "web/": "deploy-1" },
      records: {},
    });
    const clock = { ms: 0 };

    const resolution = await resolveDeployment(deps(binding, clock));

    expect(resolution).toEqual({ kind: "unavailable" });
  });

  it("keeps caches independent across apps", async () => {
    const binding = createCountingBinding({
      pointerIdentity: { "web/": "deploy-1", "admin/": "deploy-9" },
      records: {
        "web/deploy-1": makeRecord(),
        "admin/deploy-9": makeRecord({ app: "admin", identity: "deploy-9" }),
      },
    });
    const clock = { ms: 0 };

    const web = await resolveDeployment(deps(binding, clock, "web"));
    const admin = await resolveDeployment(deps(binding, clock, "admin"));

    expect(web).toEqual({ kind: "found", record: makeRecord() });
    expect(admin).toEqual({
      kind: "found",
      record: makeRecord({ app: "admin", identity: "deploy-9" }),
    });
  });

  it("resolves a preview by its full label, independently of production", async () => {
    const previewRecord = makeRecord({ identity: "preview-deploy" });
    const binding = createCountingBinding({
      pointerIdentity: {
        "web/": "deploy-1",
        "pr-12-web-abcdefghijklmnopp3347l26": "preview-deploy",
      },
      records: {
        "web/deploy-1": makeRecord(),
        "web/preview-deploy": previewRecord,
      },
    });
    const clock = { ms: 0 };

    const production = await resolveDeployment(deps(binding, clock));
    const preview = await resolveDeployment({
      binding,
      slug: "acme-web",
      host: "pr-12-web-abcdefghijklmnopp3347l26.acme.com",
      label: "pr-12-web-abcdefghijklmnopp3347l26",
      now: () => clock.ms,
    });

    expect(production).toEqual({ kind: "found", record: makeRecord() });
    expect(preview).toEqual({ kind: "found", record: previewRecord });
    expect(binding.pointerRecordCalls).toBe(1);
    expect(binding.labelRecordCalls).toBe(1);
  });

  it("keeps caches independent across projects sharing one binding", async () => {
    const records: Record<string, DeploymentRecord> = {
      acme: makeRecord({ isrPrefix: "prev/acme/web/build-1" }),
      globex: makeRecord({ isrPrefix: "prev/globex/web/build-1" }),
    };
    const binding = answerEveryRecordWith(async (args) => {
      const record = records[args.slug];
      if (!record) return { kind: "no-pointer" };
      return { kind: "record", identity: record.identity, record };
    });
    const clock = { ms: 0 };

    const acme = await resolveDeployment({
      binding,
      slug: "acme",
      host: "acme-abcdefghijklmnopaaaaaaaa.preview.ocel.app",
      label: "acme-abcdefghijklmnopaaaaaaaa",
      now: () => clock.ms,
    });
    const globex = await resolveDeployment({
      binding,
      slug: "globex",
      host: "globex-abcdefghijklmnopaaaaaaaa.preview.ocel.app",
      label: "globex-abcdefghijklmnopaaaaaaaa",
      now: () => clock.ms,
    });

    expect(acme).toEqual({ kind: "found", record: records.acme });
    expect(globex).toEqual({ kind: "found", record: records.globex });
  });

  it("caches on the host, so one host reuses the entry within the TTL", async () => {
    let calls = 0;
    const binding = answerEveryRecordWith(async () => {
      calls++;
      return { kind: "record", identity: "deploy-1", record: makeRecord() };
    });
    const clock = { ms: 0 };
    const d: DeploymentsDeps = {
      binding,
      slug: "acme",
      host: "acme.example.com",
      now: () => clock.ms,
    };

    await resolveDeployment(d);
    await resolveDeployment(d);

    expect(calls).toBe(1);
  });

  it("evicts the oldest host once the cache is full", async () => {
    const calls: Record<string, number> = {};
    const binding = answerEveryRecordWith(async (args) => {
      calls[args.slug] = (calls[args.slug] ?? 0) + 1;
      return { kind: "record", identity: args.slug, record: makeRecord() };
    });
    const clock = { ms: 0 };
    const resolve = (n: number) =>
      resolveDeployment({
        binding,
        slug: `p${n}`,
        host: `p${n}.preview.ocel.app`,
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
    const binding = answerEveryRecordWith(async (args) => {
      calls[args.slug] = (calls[args.slug] ?? 0) + 1;
      return { kind: "record", identity: args.slug, record: makeRecord() };
    });
    const clock = { ms: 0 };
    const resolve = (n: number) =>
      resolveDeployment({
        binding,
        slug: `q${n}`,
        host: `q${n}.preview.ocel.app`,
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
    const binding = answerEveryRecordWith(async (args) => {
      seen = args;
      return { kind: "ambiguous-app" };
    });
    const clock = { ms: 0 };

    const resolution = await resolveDeployment({
      binding,
      slug: "acme",
      host: "acme.example.com",
      now: () => clock.ms,
    });

    expect(seen?.app).toBeUndefined();
    expect(resolution).toEqual({ kind: "not-found" });
  });
});

import { afterEach, describe, expect, it, vi } from "vitest";

import {
  type PointerRecordResult,
  RECORD_CACHE_MAX,
  type ReleaseLookup,
  type ReleaseRecord,
  type ReleasesBinding,
  resolveRelease,
} from "../src/releases";
import { answerEveryRecordWith } from "./origin-deps";
import { routeTableKey } from "./route-table-store";

function makeRecord(over: Partial<ReleaseRecord> = {}): ReleaseRecord {
  return {
    app: "web",
    framework: "next",
    release: "deploy-1",
    buildId: "deploy-1",
    routeTable: { format: "next", key: routeTableKey() },
    functionUrls: { "/": "https://fn.example.com" },
    assetPrefix: "deploy-1",
    isrPrefix: "prod/p1/web/build-1",
    createdAt: 1_000,
    ...over,
  };
}

type CountingBinding = ReleasesBinding & {
  pointerRecordCalls: number;
  labelRecordCalls: number;
  lastReturnedRecord: boolean;
  down: boolean;
};

function createCountingBinding(opts: {
  pointerRelease: Record<string, string | undefined>;
  records: Record<string, ReleaseRecord>;
}): CountingBinding {
  const answer = (
    binding: CountingBinding,
    key: string,
    app: string,
    knownRelease?: string,
  ): PointerRecordResult => {
    if (binding.down) throw new Error("store unreachable");
    const release = opts.pointerRelease[key];
    binding.lastReturnedRecord = false;
    if (!release) return { kind: "no-pointer" };
    if (release === knownRelease) return { kind: "unchanged", release };
    const record = opts.records[`${app}/${release}`];
    if (!record) return { kind: "dangling", release };
    binding.lastReturnedRecord = true;
    return { kind: "record", release, record };
  };
  const binding: CountingBinding = {
    pointerRecordCalls: 0,
    labelRecordCalls: 0,
    lastReturnedRecord: false,
    down: false,
    async readPointerRecord(args) {
      binding.pointerRecordCalls++;
      return answer(binding, `${args.app}/`, args.app ?? "", args.knownRelease);
    },
    async readLabelRecord(args) {
      binding.labelRecordCalls++;
      return answer(binding, args.label, "web", args.knownRelease);
    },
  };
  return binding;
}

function lookupOf(
  binding: ReleasesBinding,
  clock: { ms: number },
  app = "web",
  host = `${app}.acme.com`,
): ReleaseLookup {
  return { binding, slug: "acme-web", host, app, now: () => clock.ms };
}

describe("resolveRelease", () => {
  it("resolves and returns the active release record", async () => {
    const binding = createCountingBinding({
      pointerRelease: { "web/": "deploy-1" },
      records: { "web/deploy-1": makeRecord() },
    });
    const clock = { ms: 0 };

    const resolution = await resolveRelease(lookupOf(binding, clock));

    expect(resolution).toEqual({ kind: "found", record: makeRecord() });
  });

  it("returns not-found when no active pointer exists for the app", async () => {
    const binding = createCountingBinding({ pointerRelease: {}, records: {} });
    const clock = { ms: 0 };

    const resolution = await resolveRelease(lookupOf(binding, clock));

    expect(resolution).toEqual({ kind: "not-found" });
  });

  it("reports unavailable when the store says unchanged but nothing is cached", async () => {
    const binding = answerEveryRecordWith(async () => {
      return { kind: "unchanged", release: "deploy-1" };
    });

    expect(await resolveRelease(lookupOf(binding, { ms: 0 }))).toEqual({ kind: "unavailable" });
  });

  it("serves the cached record within the TTL without calling the store", async () => {
    const binding = createCountingBinding({
      pointerRelease: { "web/": "deploy-1" },
      records: { "web/deploy-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const lookup = lookupOf(binding, clock);

    await resolveRelease(lookup);
    await resolveRelease(lookup);

    expect(binding.pointerRecordCalls).toBe(1);
  });

  it("revalidates after the TTL without re-transferring an unchanged record", async () => {
    const binding = createCountingBinding({
      pointerRelease: { "web/": "deploy-1" },
      records: { "web/deploy-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const lookup = lookupOf(binding, clock);

    await resolveRelease(lookup);
    clock.ms = 4_000; // still within the 5s TTL
    await resolveRelease(lookup);
    expect(binding.pointerRecordCalls).toBe(1);

    clock.ms = 5_001; // TTL elapsed
    const resolution = await resolveRelease(lookup);
    expect(binding.pointerRecordCalls).toBe(2);
    expect(binding.lastReturnedRecord).toBe(false);
    expect(resolution).toEqual({ kind: "found", record: makeRecord() });
  });

  it("re-reads the record when the build moves (promotion/rollback)", async () => {
    const pointerRelease: Record<string, string> = { "web/": "deploy-1" };
    const binding = createCountingBinding({
      pointerRelease,
      records: {
        "web/deploy-1": makeRecord(),
        "web/deploy-2": makeRecord({ release: "deploy-2" }),
      },
    });
    const clock = { ms: 0 };
    const lookup = lookupOf(binding, clock);

    const first = await resolveRelease(lookup);
    expect(first).toEqual({ kind: "found", record: makeRecord() });

    pointerRelease["web/"] = "deploy-2";
    clock.ms = 5_001;
    const second = await resolveRelease(lookup);

    expect(second).toEqual({
      kind: "found",
      record: makeRecord({ release: "deploy-2" }),
    });
    expect(binding.lastReturnedRecord).toBe(true);
  });

  it("serves the cached record during a transient store outage", async () => {
    const binding = createCountingBinding({
      pointerRelease: { "web/": "deploy-1" },
      records: { "web/deploy-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const lookup = lookupOf(binding, clock);

    await resolveRelease(lookup); // warms the record cache

    clock.ms = 5_001; // TTL elapsed, so the next call revalidates
    binding.down = true;
    const resolution = await resolveRelease(lookup);

    expect(resolution).toEqual({ kind: "found", record: makeRecord() });
  });

  it("returns unavailable on a cold isolate when the store is unreachable", async () => {
    const binding = createCountingBinding({ pointerRelease: {}, records: {} });
    binding.down = true;
    const clock = { ms: 0 };

    const resolution = await resolveRelease(lookupOf(binding, clock));

    expect(resolution).toEqual({ kind: "unavailable" });
  });

  afterEach(() => vi.restoreAllMocks());

  it("logs a store read that failed before answering unavailable", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const binding = createCountingBinding({ pointerRelease: {}, records: {} });
    binding.down = true;
    const clock = { ms: 0 };

    const resolution = await resolveRelease(lookupOf(binding, clock));

    expect(resolution).toEqual({ kind: "unavailable" });
    expect(errors).toHaveBeenCalledTimes(1);
    expect(errors.mock.calls[0]?.[0]).toContain("acme-web/web");
    expect(errors.mock.calls[0]?.[0]).toContain("answering 503");
    expect(errors.mock.calls[0]?.[1]).toEqual(new Error("store unreachable"));
  });

  it("logs a store read that failed before serving the record it cached", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const binding = createCountingBinding({
      pointerRelease: { "web/": "deploy-1" },
      records: { "web/deploy-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const lookup = lookupOf(binding, clock);
    await resolveRelease(lookup);

    clock.ms = 5_001;
    binding.down = true;
    const resolution = await resolveRelease(lookup);

    expect(resolution).toEqual({ kind: "found", record: makeRecord() });
    expect(errors).toHaveBeenCalledTimes(1);
    expect(errors.mock.calls[0]?.[0]).toContain("acme-web/web");
    expect(errors.mock.calls[0]?.[0]).toContain("cached 5s ago");
  });

  it("logs nothing when the store answers", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const binding = createCountingBinding({
      pointerRelease: { "web/": "deploy-1" },
      records: { "web/deploy-1": makeRecord() },
    });
    const clock = { ms: 0 };
    const lookup = lookupOf(binding, clock);
    await resolveRelease(lookup);
    clock.ms = 5_001;
    await resolveRelease(lookup);

    expect(errors).not.toHaveBeenCalled();
  });

  it("returns unavailable when the pointer names a build with no record", async () => {
    const binding = createCountingBinding({
      pointerRelease: { "web/": "deploy-1" },
      records: {},
    });
    const clock = { ms: 0 };

    const resolution = await resolveRelease(lookupOf(binding, clock));

    expect(resolution).toEqual({ kind: "unavailable" });
  });

  it("keeps caches independent across apps", async () => {
    const binding = createCountingBinding({
      pointerRelease: { "web/": "deploy-1", "admin/": "deploy-9" },
      records: {
        "web/deploy-1": makeRecord(),
        "admin/deploy-9": makeRecord({ app: "admin", release: "deploy-9" }),
      },
    });
    const clock = { ms: 0 };

    const web = await resolveRelease(lookupOf(binding, clock, "web"));
    const admin = await resolveRelease(lookupOf(binding, clock, "admin"));

    expect(web).toEqual({ kind: "found", record: makeRecord() });
    expect(admin).toEqual({
      kind: "found",
      record: makeRecord({ app: "admin", release: "deploy-9" }),
    });
  });

  it("resolves a preview by its full label, independently of production", async () => {
    const previewRecord = makeRecord({ release: "preview-deploy" });
    const binding = createCountingBinding({
      pointerRelease: {
        "web/": "deploy-1",
        "pr-12-web-abcdefghijklmnopp3347l26": "preview-deploy",
      },
      records: {
        "web/deploy-1": makeRecord(),
        "web/preview-deploy": previewRecord,
      },
    });
    const clock = { ms: 0 };

    const production = await resolveRelease(lookupOf(binding, clock));
    const preview = await resolveRelease({
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
    const records: Record<string, ReleaseRecord> = {
      acme: makeRecord({ isrPrefix: "prev/acme/web/build-1" }),
      globex: makeRecord({ isrPrefix: "prev/globex/web/build-1" }),
    };
    const binding = answerEveryRecordWith(async (args) => {
      const record = records[args.slug];
      if (!record) return { kind: "no-pointer" };
      return { kind: "record", release: record.release, record };
    });
    const clock = { ms: 0 };

    const acme = await resolveRelease({
      binding,
      slug: "acme",
      host: "acme-abcdefghijklmnopaaaaaaaa.preview.ocel.app",
      label: "acme-abcdefghijklmnopaaaaaaaa",
      now: () => clock.ms,
    });
    const globex = await resolveRelease({
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
      return { kind: "record", release: "deploy-1", record: makeRecord() };
    });
    const clock = { ms: 0 };
    const lookup: ReleaseLookup = {
      binding,
      slug: "acme",
      host: "acme.example.com",
      now: () => clock.ms,
    };

    await resolveRelease(lookup);
    await resolveRelease(lookup);

    expect(calls).toBe(1);
  });

  it("evicts the oldest host once the cache is full", async () => {
    const calls: Record<string, number> = {};
    const binding = answerEveryRecordWith(async (args) => {
      calls[args.slug] = (calls[args.slug] ?? 0) + 1;
      return { kind: "record", release: args.slug, record: makeRecord() };
    });
    const clock = { ms: 0 };
    const resolve = (n: number) =>
      resolveRelease({
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
      return { kind: "record", release: args.slug, record: makeRecord() };
    });
    const clock = { ms: 0 };
    const resolve = (n: number) =>
      resolveRelease({
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

    const resolution = await resolveRelease({
      binding,
      slug: "acme",
      host: "acme.example.com",
      now: () => clock.ms,
    });

    expect(seen?.app).toBeUndefined();
    expect(resolution).toEqual({ kind: "not-found" });
  });
});

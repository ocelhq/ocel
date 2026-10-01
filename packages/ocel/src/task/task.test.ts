import { type Timestamp, timestampDate } from "@bufbuild/protobuf/wkt";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { ResourceType } from "../gen/proto/app/resources/v1/resources_pb.js";
import {
  type BatchTriggerRequest,
  TaskService,
  type TriggerRequest,
} from "../gen/proto/app/task/v1/task_pb.js";
import { Lane } from "../gen/proto/app/topic/v1/topic_pb.js";
import { type RuntimeProxy, serveRuntimeProxy } from "../testing/runtime-proxy.js";

const declareMock = vi.hoisted(() => vi.fn((_req: unknown) => Promise.resolve({})));

vi.mock("../runtime/rpc", () => ({
  rpc: { resource: { declare: declareMock } },
}));

const { task, UnprovisionedResourceError } = await import("./index.js");
const { worker } = await import("../worker/index.js");

describe("task discovery declare", () => {
  beforeEach(() => {
    declareMock.mockClear();
  });

  it("declares a TASK under its own name with every option unset when none is given", () => {
    task("resize-image", { run: async () => {} });

    expect(declareMock).toHaveBeenCalledWith({
      resource: { name: "resize-image", type: ResourceType.TASK },
      config: {
        case: "task",
        value: {
          schema: "",
          ordered: false,
          retry: undefined,
          concurrency: 0,
          maxDuration: undefined,
          ttl: undefined,
          batch: undefined,
          worker: "",
          cron: "",
        },
      },
      source: expect.any(String),
    });
  });

  it("declares every option it was given, durations as seconds and nanos", () => {
    const media = worker("media");

    task("transcode", {
      run: async (_videos: string[]) => {},
      retry: { maxAttempts: 5, minDelay: "500ms", maxDelay: "2m" },
      concurrency: 10,
      maxDuration: 900,
      ttl: "1d",
      ordered: true,
      batch: { size: 25, timeout: "1.5s" },
      worker: media,
      cron: "0 * * * *",
    });

    expect(declareMock).toHaveBeenCalledWith({
      resource: { name: "transcode", type: ResourceType.TASK },
      config: {
        case: "task",
        value: {
          schema: "",
          ordered: true,
          retry: {
            maxAttempts: 5,
            minDelay: { seconds: 0n, nanos: 500_000_000 },
            maxDelay: { seconds: 120n, nanos: 0 },
          },
          concurrency: 10,
          maxDuration: { seconds: 900n, nanos: 0 },
          ttl: { seconds: 86_400n, nanos: 0 },
          batch: { size: 25, timeout: { seconds: 1n, nanos: 500_000_000 } },
          worker: "media",
          cron: "0 * * * *",
        },
      },
      source: expect.any(String),
    });
  });

  it("declares the JSON Schema of a payload schema's input", () => {
    task("send-email", {
      schema: z.object({ to: z.email(), retries: z.number().default(0) }),
      run: async () => {},
    });

    const [request] = declareMock.mock.calls.at(-1) as [{ config: { value: { schema: string } } }];
    const schema = JSON.parse(request.config.value.schema);
    expect(schema).toMatchObject({
      $schema: "https://json-schema.org/draft/2020-12/schema",
      type: "object",
      properties: { to: { type: "string" }, retries: { type: "number" } },
      required: ["to"],
    });
  });

  it("declares no JSON Schema for a schema that cannot describe itself as one", () => {
    task("plain", {
      schema: {
        "~standard": { version: 1, vendor: "custom", validate: (value) => ({ value }) },
      },
      run: async () => {},
    });

    expect(declareMock).toHaveBeenLastCalledWith(
      expect.objectContaining({
        config: expect.objectContaining({ value: expect.objectContaining({ schema: "" }) }),
      }),
    );
  });

  it("refuses a duration it cannot read, naming it", () => {
    expect(() => task("bad", { run: async () => {}, ttl: "soon" as never })).toThrow(
      '"soon" is not a duration',
    );
  });
});

describe("a task at runtime", () => {
  let proxy: RuntimeProxy;
  const triggers: TriggerRequest[] = [];
  const batches: BatchTriggerRequest[] = [];

  beforeEach(async () => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv(
      "OCEL_RESOURCE_TASK_resize-image",
      JSON.stringify({ name: "resize-image", task: { task: "shop-prod-resize-image" } }),
    );
    triggers.length = 0;
    batches.length = 0;
    proxy = await serveRuntimeProxy((router) =>
      router.service(TaskService, {
        trigger: (req) => {
          triggers.push(req);
          return { id: "run_1" };
        },
        batchTrigger: (req) => {
          batches.push(req);
          return { ids: req.items.map((_, i) => `run_${i + 1}`) };
        },
      }),
    );
  });

  afterEach(async () => {
    vi.unstubAllEnvs();
    await proxy.close();
  });

  it("triggers a run of its bound task with the payload as JSON, and answers the run's id", async () => {
    const resize = task("resize-image", { run: async (_image: { url: string }) => {} });

    const run = await resize.trigger({ url: "s3://a.png" });

    expect(run).toEqual({ id: "run_1" });
    expect(triggers).toHaveLength(1);
    expect(triggers[0]?.task).toBe("shop-prod-resize-image");
    expect(new TextDecoder().decode(triggers[0]?.payload)).toBe('{"url":"s3://a.png"}');
    expect(proxy.authorizations).toEqual(["Bearer session-token"]);
  });

  it("sends every trigger option, the delay as the time the run is due", async () => {
    vi.useFakeTimers({ now: new Date("2026-03-01T00:00:00Z"), toFake: ["Date"] });
    const resize = task("resize-image", { run: async () => {} });

    await resize.trigger(null, {
      delay: "1h",
      ttl: "10m",
      idempotencyKey: "order-1",
      idempotencyKeyTTL: "1d",
      debounce: { key: "user-1", delay: 5 },
      key: "user-1",
      lane: "high",
      maxAttempts: 2,
      tags: ["user:1"],
      metadata: { source: "upload", size: 3 },
    });
    vi.useRealTimers();

    const options = triggers[0]?.options;
    expect(timestampDate(options?.dueAt as Timestamp)).toEqual(new Date("2026-03-01T01:00:00Z"));
    expect(options?.ttl?.seconds).toBe(600n);
    expect(options?.idempotencyKey).toBe("order-1");
    expect(options?.idempotencyKeyTtl?.seconds).toBe(86_400n);
    expect(options?.debounce?.key).toBe("user-1");
    expect(options?.debounce?.delay?.seconds).toBe(5n);
    expect(options?.key).toBe("user-1");
    expect(options?.lane).toBe(Lane.HIGH);
    expect(options?.maxAttempts).toBe(2);
    expect(options?.tags).toEqual(["user:1"]);
    expect(options?.metadata).toEqual({ source: "upload", size: 3 });
  });

  it("sends a delay given as a date as that date", async () => {
    const resize = task("resize-image", { run: async () => {} });

    await resize.trigger(1, { delay: new Date("2027-01-01T00:00:00Z") });

    expect(timestampDate(triggers[0]?.options?.dueAt as Timestamp)).toEqual(
      new Date("2027-01-01T00:00:00Z"),
    );
  });

  it("leaves every trigger option unset when none is given", async () => {
    const resize = task("resize-image", { run: async () => {} });

    await resize.trigger(1);

    const options = triggers[0]?.options;
    expect(options?.dueAt).toBeUndefined();
    expect(options?.ttl).toBeUndefined();
    expect(options?.debounce).toBeUndefined();
    expect(options?.lane).toBe(Lane.UNSPECIFIED);
    expect(options?.maxAttempts).toBe(0);
    expect(options?.metadata).toBeUndefined();
  });

  it("triggers a batch in one request, and answers a handle per item in order", async () => {
    const resize = task("resize-image", { run: async () => {} });

    const handles = await resize.batchTrigger([
      { payload: "a" },
      { payload: "b", options: { key: "k" } },
    ]);

    expect(handles).toEqual([{ id: "run_1" }, { id: "run_2" }]);
    expect(batches[0]?.task).toBe("shop-prod-resize-image");
    expect(batches[0]?.items.map((item) => new TextDecoder().decode(item.payload))).toEqual([
      '"a"',
      '"b"',
    ]);
    expect(batches[0]?.items[1]?.options?.key).toBe("k");
  });

  it("refuses a payload over 256 KiB before sending anything", async () => {
    const resize = task("resize-image", { run: async () => {} });

    await expect(resize.trigger("x".repeat(262_144))).rejects.toThrow(
      "a payload is at most 262144 bytes (256 KiB) of JSON",
    );
    expect(triggers).toHaveLength(0);
  });
});

describe("a task during discovery", () => {
  it("refuses to trigger, naming the task and the operation", async () => {
    const resize = task("resize-image", { run: async () => {} });

    await expect(resize.trigger(1)).rejects.toThrow(
      "'task(\"resize-image\")' cannot be used during discovery: tried to access 'trigger'",
    );
    await expect(resize.batchTrigger([])).rejects.toThrow(UnprovisionedResourceError);
  });
});

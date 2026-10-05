import { create } from "@bufbuild/protobuf";
import { type Timestamp, timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  type ListRunsRequest,
  type RescheduleRunRequest,
  RunSchema,
  RunStatus,
  TaskService,
} from "../gen/proto/app/task/v1/task_pb.js";
import { EXACT_JSON } from "../testing/exact-json.js";
import { type RuntimeProxy, serveRuntimeProxy } from "../testing/runtime-proxy.js";

vi.mock("../runtime/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { JsonText, runs, task } = await import("./index.js");

const storedRun = create(RunSchema, {
  id: "run_1",
  task: "resize-image",
  status: RunStatus.COMPLETED,
  payload: new TextEncoder().encode('{"url":"a.png","width":2.0}'),
  output: new TextEncoder().encode("3"),
  attempts: 2,
  tags: ["user:1"],
  metadata: new TextEncoder().encode('{"source":"upload"}'),
  createdAt: timestampFromDate(new Date("2026-01-01T00:00:00Z")),
  finishedAt: timestampFromDate(new Date("2026-01-01T00:01:00Z")),
});

const exactRun = create(RunSchema, {
  ...storedRun,
  id: "run_exact",
  payload: new TextEncoder().encode(EXACT_JSON),
  output: new TextEncoder().encode(EXACT_JSON),
});

const queuedRun = create(RunSchema, {
  ...storedRun,
  id: "run_queued",
  status: RunStatus.QUEUED,
  output: new Uint8Array(),
  finishedAt: undefined,
});

const storedRuns = new Map([storedRun, exactRun, queuedRun].map((run) => [run.id, run]));

describe("runs", () => {
  let proxy: RuntimeProxy;
  const lists: ListRunsRequest[] = [];
  const reschedules: RescheduleRunRequest[] = [];
  const ids: string[] = [];

  beforeEach(async () => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv(
      "OCEL_RESOURCE_TASK_resize-image",
      JSON.stringify({ name: "resize-image", task: {} }),
    );
    lists.length = 0;
    reschedules.length = 0;
    ids.length = 0;
    proxy = await serveRuntimeProxy((router) =>
      router.service(TaskService, {
        retrieveRun: ({ id }) => {
          ids.push(id);
          return { run: storedRuns.get(id) };
        },
        listRuns: (req) => {
          lists.push(req);
          return { runs: [storedRun], nextCursor: "next" };
        },
        cancelRun: ({ id }) => {
          ids.push(id);
          return { run: { ...storedRun, status: RunStatus.CANCELED } };
        },
        replayRun: ({ id }) => {
          ids.push(id);
          return { id: "run_2" };
        },
        rescheduleRun: (req) => {
          reschedules.push(req);
          return { run: { ...storedRun, status: RunStatus.DELAYED } };
        },
      }),
    );
  });

  afterEach(async () => {
    vi.unstubAllEnvs();
    await proxy.close();
  });

  it("retrieves a run as a record with its JSON decoded and its times as dates", async () => {
    expect(await runs.retrieve("run_1")).toEqual({
      id: "run_1",
      task: "resize-image",
      status: "COMPLETED",
      payload: { url: "a.png", width: 2 },
      payloadJson: new JsonText('{"url":"a.png","width":2.0}'),
      output: 3,
      outputJson: new JsonText("3"),
      error: undefined,
      attempts: 2,
      tags: ["user:1"],
      metadata: { source: "upload" },
      createdAt: new Date("2026-01-01T00:00:00Z"),
      dueAt: undefined,
      startedAt: undefined,
      finishedAt: new Date("2026-01-01T00:01:00Z"),
      expiresAt: undefined,
    });
    expect(ids).toEqual(["run_1"]);
    expect(proxy.authorizations).toEqual(["Bearer session-token"]);
  });

  it("retrieves a run's payload and output as the JSON text they were recorded as", async () => {
    const run = await runs.retrieve("run_exact");

    expect(run.payloadJson.text).toBe(EXACT_JSON);
    expect(run.outputJson?.text).toBe(EXACT_JSON);
  });

  it("retrieves a run that has recorded no output with no output JSON text", async () => {
    const run = await runs.retrieve("run_queued");

    expect(run.output).toBeNull();
    expect(run.outputJson).toBeUndefined();
  });

  it("lists the runs of a task handle by its declared name, with statuses, tags and paging", async () => {
    const resize = task("resize-image", { run: async () => {} });

    const page = await runs.list({
      task: resize,
      status: ["FAILED", "TIMED_OUT"],
      tags: ["user:1"],
      cursor: "c",
      limit: 50,
    });

    expect(page.nextCursor).toBe("next");
    expect(page.runs.map((run) => run.id)).toEqual(["run_1"]);
    expect(lists[0]).toMatchObject({
      task: "resize-image",
      statuses: [RunStatus.FAILED, RunStatus.TIMED_OUT],
      tags: ["user:1"],
      cursor: "c",
      limit: 50,
    });
  });

  it("lists the runs of a task named by its declared name", async () => {
    await runs.list({ task: "resize-image", status: "QUEUED" });

    expect(lists[0]).toMatchObject({
      task: "resize-image",
      statuses: [RunStatus.QUEUED],
    });
  });

  it("lists every task's runs when no task is given", async () => {
    await runs.list();

    expect(lists[0]).toMatchObject({ task: "", statuses: [], tags: [], cursor: "", limit: 0 });
  });

  it("cancels a run and answers the record", async () => {
    expect((await runs.cancel("run_1")).status).toBe("CANCELED");
    expect(ids).toEqual(["run_1"]);
  });

  it("replays a run and answers the new run's handle", async () => {
    expect(await runs.replay("run_1")).toEqual({ id: "run_2" });
  });

  it("reschedules a run to the time its delay names", async () => {
    vi.useFakeTimers({ now: new Date("2026-03-01T00:00:00Z"), toFake: ["Date"] });
    const run = await runs.reschedule("run_1", { delay: "30m" });
    vi.useRealTimers();

    expect(run.status).toBe("DELAYED");
    expect(reschedules[0]?.id).toBe("run_1");
    expect(timestampDate(reschedules[0]?.dueAt as Timestamp)).toEqual(
      new Date("2026-03-01T00:30:00Z"),
    );
  });
});

describe("runs during discovery", () => {
  it("refuse every operation, naming it", async () => {
    await expect(runs.retrieve("run_1")).rejects.toThrow(
      "'runs' cannot be used during discovery: tried to access 'retrieve'",
    );
    await expect(runs.list()).rejects.toThrow("tried to access 'list'");
  });
});

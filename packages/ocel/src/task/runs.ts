import { type JsonObject, toJson } from "@bufbuild/protobuf";
import { type Timestamp, timestampDate, ValueSchema } from "@bufbuild/protobuf/wkt";
import { type Client, createClient } from "@connectrpc/connect";
import { getConfig } from "../binding/binding.js";
import { unprovisioned, unprovisionedPhase } from "../binding/unprovisioned.js";
import { type Duration, encodeDueAt } from "../delivery/duration.js";
import {
  type Run as ProtoRun,
  RunStatus as ProtoRunStatus,
  TaskService,
} from "../gen/proto/app/task/v1/task_pb.js";
import { createRuntimeTransport } from "../runtime/transport.js";
import type { RunHandle, Task } from "./task.js";

/** Where a run is in its life. */
export type RunStatus =
  | "DELAYED"
  | "QUEUED"
  | "EXECUTING"
  | "COMPLETED"
  | "FAILED"
  | "CANCELED"
  | "EXPIRED"
  | "TIMED_OUT";

/** A run of a task, as the runs store records it. */
export interface Run {
  /** The run's id. */
  id: string;
  /** The task the run belongs to. */
  task: string;
  /** Where the run is in its life. */
  status: RunStatus;
  /** The payload it was triggered with. */
  payload: unknown;
  /** The output its `run` returned, once it completed. */
  output: unknown;
  /** The error that failed it, once it failed. */
  error: string | undefined;
  /** How many attempts were made. */
  attempts: number;
  /** The tags it was triggered with. */
  tags: string[];
  /** The metadata it was triggered with. */
  metadata: JsonObject;
  /** When it was triggered. */
  createdAt: Date | undefined;
  /** When it is or was due. */
  dueAt: Date | undefined;
  /** When its first attempt started. */
  startedAt: Date | undefined;
  /** When it ended. */
  finishedAt: Date | undefined;
  /** When it expires if it has not started. */
  expiresAt: Date | undefined;
}

/** Which runs to list. */
export interface ListRunsOptions {
  /** Only the runs of this task, given as its handle or its declared name. */
  task?: Task | string;
  /** Only the runs in this status, or in any of these. */
  status?: RunStatus | RunStatus[];
  /** Only the runs carrying every one of these tags. */
  tags?: string[];
  /** The `nextCursor` of the previous page. */
  cursor?: string;
  /** The most runs on the page. */
  limit?: number;
}

/** One page of runs. */
export interface RunPage {
  /** The runs on this page. */
  runs: Run[];
  /** Passed as `cursor` to read the next page; empty on the last one. */
  nextCursor: string;
}

/** When a rescheduled run is due. */
export interface RescheduleOptions {
  /** How long from now the run is due, or the date it is due at. */
  delay: Duration | Date;
}

const decodeTimestamp = (timestamp: Timestamp | undefined) =>
  timestamp ? timestampDate(timestamp) : undefined;

function decodeRun(run: ProtoRun | undefined): Run {
  if (!run) throw new Error("the ocel runtime answered without a run");
  return {
    id: run.id,
    task: run.task,
    status: ProtoRunStatus[run.status] as RunStatus,
    payload: run.payload ? toJson(ValueSchema, run.payload) : null,
    output: run.output ? toJson(ValueSchema, run.output) : null,
    error: run.error || undefined,
    attempts: run.attempts,
    tags: run.tags,
    metadata: run.metadata ?? {},
    createdAt: decodeTimestamp(run.createdAt),
    dueAt: decodeTimestamp(run.dueAt),
    startedAt: decodeTimestamp(run.startedAt),
    finishedAt: decodeTimestamp(run.finishedAt),
    expiresAt: decodeTimestamp(run.expiresAt),
  };
}

function encodeRunStatus(status: RunStatus): ProtoRunStatus {
  const value = ProtoRunStatus[status];
  if (value === undefined) {
    throw new Error(`"${status}" is not a run status`);
  }
  return value;
}

function createTaskClient(operation: string): Client<typeof TaskService> {
  if (unprovisionedPhase()) {
    throw unprovisioned("runs", operation);
  }
  return createClient(TaskService, createRuntimeTransport());
}

function getBoundTaskName(task: Task | string | undefined): string {
  if (task === undefined) return "";
  return getConfig(typeof task === "string" ? task : task.name, "task").task;
}

/** Reads and acts on the runs of this app's tasks. */
export const runs = {
  /** The run with this id. */
  async retrieve(id: string): Promise<Run> {
    const { run } = await createTaskClient("retrieve").retrieveRun({ id });
    return decodeRun(run);
  },

  /** One page of runs, newest first. */
  async list(options: ListRunsOptions = {}): Promise<RunPage> {
    const client = createTaskClient("list");
    const statuses =
      options.status === undefined
        ? []
        : (Array.isArray(options.status) ? options.status : [options.status]).map(encodeRunStatus);
    const page = await client.listRuns({
      task: getBoundTaskName(options.task),
      statuses,
      tags: options.tags ?? [],
      cursor: options.cursor ?? "",
      limit: options.limit ?? 0,
    });
    return { runs: page.runs.map(decodeRun), nextCursor: page.nextCursor };
  },

  /** Stops the run from making another attempt; an attempt in progress sees its signal abort. */
  async cancel(id: string): Promise<Run> {
    const { run } = await createTaskClient("cancel").cancelRun({ id });
    return decodeRun(run);
  },

  /** Starts a new run with the same payload and options, and answers its handle. */
  async replay(id: string): Promise<RunHandle> {
    const { id: replayed } = await createTaskClient("replay").replayRun({ id });
    return { id: replayed };
  },

  /** Moves a run that has not started to a new due time. */
  async reschedule(id: string, options: RescheduleOptions): Promise<Run> {
    const { run } = await createTaskClient("reschedule").rescheduleRun({
      id,
      dueAt: encodeDueAt(options.delay),
    });
    return decodeRun(run);
  },
};

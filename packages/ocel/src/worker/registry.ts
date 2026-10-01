import type { StandardSchemaV1 } from "@standard-schema/spec";
import type { TaskHooks } from "../task/hooks.js";
import type { RunContext, RunOptions } from "./context.js";

export const DEFAULT_WORKER = "worker";

export interface Registration {
  kind: "task" | "consumer";
  topic: string;
  name: string;
  worker: string;
  batch: boolean;
  schema: StandardSchemaV1 | undefined;
  run: (payload: unknown, options: RunOptions) => unknown;
  hooks: TaskHooks<unknown, unknown>;
}

/** Wraps every run a worker serves, of its tasks and its consumers alike; `next` runs it. */
export type WorkerMiddleware = (args: { ctx: RunContext; next: () => Promise<void> }) => unknown;

export interface WorkerRegistration {
  concurrency: number | undefined;
  onStart: (() => unknown) | undefined;
  middleware: WorkerMiddleware | undefined;
}

interface Registry {
  runs: Map<string, Registration>;
  workers: Map<string, WorkerRegistration>;
}

const registryKey = Symbol.for("ocel.worker.registry");

function ensureRegistry(): Registry {
  const global = globalThis as { [registryKey]?: Registry };
  global[registryKey] ??= { runs: new Map(), workers: new Map() };
  return global[registryKey];
}

function formatRunKey(topic: string, consumer: string): string {
  return `${topic}\u0000${consumer}`;
}

export function registerRun(registration: Registration): void {
  ensureRegistry().runs.set(formatRunKey(registration.topic, registration.name), registration);
}

export function findRegistration(topic: string, consumer: string): Registration | undefined {
  return ensureRegistry().runs.get(formatRunKey(topic, consumer));
}

export function registerWorker(name: string, worker: WorkerRegistration): void {
  ensureRegistry().workers.set(name, worker);
}

export function findWorker(name: string): WorkerRegistration | undefined {
  return ensureRegistry().workers.get(name);
}

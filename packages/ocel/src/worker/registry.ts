import type { StandardSchemaV1 } from "@standard-schema/spec";
import { isDiscovering } from "../binding/unprovisioned.js";
import { declarationSite } from "../declaration/callsite.js";
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
  runs: Map<string, Registration & { source: string }>;
  workers: Map<string, WorkerRegistration & { source: string }>;
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

function refuseRedeclared(declared: { source: string } | undefined, source: string, what: string) {
  if (!declared || declared.source === source || isDiscovering()) return;
  throw new Error(
    `${what} is declared twice, and a ${what.split(" ")[0]} is declared once: at ${declared.source || "an unknown line"} and at ${source || "an unknown line"}`,
  );
}

export function registerRun(registration: Registration): void {
  const runs = ensureRegistry().runs;
  const key = formatRunKey(registration.topic, registration.name);
  const source = declarationSite();
  refuseRedeclared(
    runs.get(key),
    source,
    registration.kind === "task"
      ? `task "${registration.name}"`
      : `consumer "${registration.name}" of topic "${registration.topic}"`,
  );
  runs.set(key, { ...registration, source });
}

export function findRegistration(topic: string, consumer: string): Registration | undefined {
  return ensureRegistry().runs.get(formatRunKey(topic, consumer));
}

export function registerWorker(name: string, worker: WorkerRegistration): void {
  const workers = ensureRegistry().workers;
  const source = declarationSite();
  refuseRedeclared(workers.get(name), source, `worker "${name}"`);
  workers.set(name, { ...worker, source });
}

export function findWorker(name: string): WorkerRegistration | undefined {
  return ensureRegistry().workers.get(name);
}

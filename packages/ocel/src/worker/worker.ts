import { ResourceType } from "../gen/proto/app/resources/v1/resources_pb.js";
import { declarationSite } from "../utils/callsite.js";
import { defer } from "../utils/defer.js";
import { rpc } from "../utils/rpc.js";
import { registerWorker, type WorkerMiddleware } from "./registry.js";

/** How a worker is declared. */
export interface WorkerOptions {
  /** The most runs this worker serves at once, across all its tasks and consumers. */
  concurrency?: number;
  /** Runs once per process, before the first run it serves. */
  onStart?: () => unknown;
  /** Wraps every run this worker serves. */
  middleware?: WorkerMiddleware;
}

/** A declared worker: the app that serves the tasks and consumers placed on it. */
export class Worker {
  /** The name this worker was declared under, and the `ocel.json` app it joins. */
  readonly name: string;

  constructor(name: string, options: WorkerOptions) {
    this.name = name;
    registerWorker(name, {
      concurrency: options.concurrency,
      onStart: options.onStart,
      middleware: options.middleware,
    });
    if (process.env.OCEL_PHASE === "discovery") {
      defer(
        rpc.resource.declare({
          resource: { name, type: ResourceType.WORKER },
          config: { case: "worker", value: { concurrency: options.concurrency ?? 0 } },
          source: declarationSite(),
        }),
      );
    }
  }
}

/**
 * Declares a worker. A task or consumer runs on it when declared with `worker: <this>`;
 * without one, it runs on the worker named `worker`.
 */
export function worker(name: string, options: WorkerOptions = {}): Worker {
  return new Worker(name, options);
}

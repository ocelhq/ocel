import type { Fetch } from "../checks/context";
import type { Evidence } from "../evidence";
import type { Fixture, Lane, TargetName, Variant } from "../matrix/types";
import type { PrepareFailures } from "../prepare";

export type Deployment = {
  baseUrl: (app: string) => string;
  fetch: Fetch;
};

export type CellContext = {
  fixture: Fixture;
  name: string;
  variant: Variant;
  dir: string;
  slug: string;
  runId: string;
  evidence: Evidence;
};

export interface Sweeper {
  list(): Promise<string[]>;
  exists(slug: string): Promise<boolean>;
  sweepStale(runId: string): Promise<void>;
  sweepRun(runId: string): Promise<void>;
}

export interface Target {
  readonly name: TargetName;
  readonly workers: number;
  readonly maxRequestBodyBytes: number;
  readonly stepTimeoutMs: number;
  readonly sweeper: Sweeper;
  detectLane(): Promise<Lane>;
  prepareLane(): Promise<PrepareFailures>;
  prepareProcess(): Promise<void>;
  deploy(cell: CellContext): Promise<Deployment>;
  destroy(cell: CellContext): Promise<void>;
}

export interface ReleaseCycle {
  redeploy(cell: CellContext, greeting: string): Promise<Deployment>;
  rollback(cell: CellContext, greeting: string): Promise<Deployment>;
}
export function hasReleaseCycle<T extends object>(target: T): target is T & ReleaseCycle {
  const cycled = target as Partial<ReleaseCycle>;
  return typeof cycled.redeploy === "function" && typeof cycled.rollback === "function";
}

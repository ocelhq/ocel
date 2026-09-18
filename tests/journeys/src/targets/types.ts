import type { Fetch } from "../checks/context";
import type { Lane, TargetName } from "../matrix/types";
import type { PrepareFailures } from "../prepare";
import type { CellUnderTest } from "../run/cellRun";

export type Deployment = {
  baseUrl: (app: string) => string;
  fetch: Fetch;
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
  deploy(cell: CellUnderTest): Promise<Deployment>;
  destroy(cell: CellUnderTest): Promise<void>;
}

export interface ReleaseCycle {
  redeploy(cell: CellUnderTest, greeting: string): Promise<Deployment>;
  rollback(cell: CellUnderTest, greeting: string): Promise<Deployment>;
}
export function hasReleaseCycle<T extends object>(target: T): target is T & ReleaseCycle {
  const cycled = target as Partial<ReleaseCycle>;
  return typeof cycled.redeploy === "function" && typeof cycled.rollback === "function";
}

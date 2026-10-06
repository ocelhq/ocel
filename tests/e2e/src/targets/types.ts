import type { Fetch } from "../checks/context";
import type { Lane, TargetName } from "../matrix/types";
import type { Plan } from "../plan";
import type { PrepareFailures } from "../prepare";
import type { CellUnderTest } from "../run/cellRun";

export type Deployment = {
  baseUrl: (app: string) => string;
  fetch: Fetch;
  reach?: (url: string) => Promise<string>;
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
  prepareLane(planned: Pick<Plan, "cells">): Promise<PrepareFailures>;
  prepareProcess(): Promise<void>;
  finishLane?(): Promise<void>;
  deploy(cell: CellUnderTest): Promise<Deployment>;
  destroy(cell: CellUnderTest): Promise<void>;
}

export interface ReleaseCycle {
  redeploy(cell: CellUnderTest, greeting: string): Promise<Deployment>;
  rollback(cell: CellUnderTest, greeting: string): Promise<Deployment>;
}

export type PreviewRelease = {
  app: string;
  urls: string[];
  deploymentUrl: string;
  deploymentId: string;
};

export interface Previews {
  readonly previewLanes: Lane[];
  readonly previewStepTimeoutMs: number;
  previewUp(cell: CellUnderTest, name: string): Promise<PreviewRelease[]>;
  previewPrune(cell: CellUnderTest, name: string, keep: number): Promise<void>;
  previewRemove(cell: CellUnderTest, name: string): Promise<void>;
}

export function hasPreviews<T extends object>(target: T): target is T & Previews {
  const previewing = target as Partial<Previews>;
  return (
    typeof previewing.previewUp === "function" &&
    typeof previewing.previewPrune === "function" &&
    typeof previewing.previewRemove === "function"
  );
}

export function previewsOn(target: object, lane: Lane): boolean {
  return hasPreviews(target) && target.previewLanes.includes(lane);
}

export interface Exposure {
  readExposed(cell: CellUnderTest): Promise<string>;
}

export function hasExposure<T extends object>(target: T): target is T & Exposure {
  return typeof (target as Partial<Exposure>).readExposed === "function";
}

export interface Restart {
  restart(cell: CellUnderTest): Promise<Deployment>;
}

export function hasRestart<T extends object>(target: T): target is T & Restart {
  return typeof (target as Partial<Restart>).restart === "function";
}

export function hasReleaseCycle<T extends object>(target: T): target is T & ReleaseCycle {
  const cycled = target as Partial<ReleaseCycle>;
  return typeof cycled.redeploy === "function" && typeof cycled.rollback === "function";
}

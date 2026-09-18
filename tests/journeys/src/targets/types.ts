import type { Fetch } from "../contract";
import type { Evidence } from "../evidence";
import type { Fixture, Lane, Leg, TargetName, Variant } from "../matrix/types";
import type { PrepareFailures } from "../prepare";

export type Deployment = {
  baseUrl: (app: string) => string;
  fetch: Fetch;
};

export type CellContext = {
  fixture: Fixture;
  name: string;
  variant?: Variant;
  dir: string;
  slug: string;
  runId: string;
  evidence: Evidence;
};

export type Target = {
  name: TargetName;
  concurrency: number;
  largeBodyBytes: number;
  legTimeoutMs: number;
  legs: Leg[];
  guard: () => Promise<Lane>;
  // biome-ignore lint/suspicious/noConfusingVoidType: a target with nothing to prepare resolves to nothing
  prepare?: () => Promise<PrepareFailures | void>;
  setup: () => Promise<void>;
  up: (cell: CellContext) => Promise<Deployment>;
  redeploy?: (cell: CellContext, greeting: string) => Promise<Deployment>;
  rollback?: (cell: CellContext, greeting: string) => Promise<Deployment>;
  destroy: (cell: CellContext) => Promise<void>;
  list: () => Promise<string[]>;
  stands: (slug: string) => Promise<boolean>;
  sweep: (runId: string) => Promise<void>;
  sweepOwn: (runId: string) => Promise<void>;
};

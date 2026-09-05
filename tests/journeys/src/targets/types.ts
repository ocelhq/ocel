import type { Fetch } from "../contract";
import type { Evidence } from "../evidence";
import type { ExpectationEnvironment } from "../expectations/types";
import type { PrepareFailures } from "../prepare";
import type { FixtureSpec, Leg, TargetName } from "../spec";
import type { Variant } from "../variants";

export type Deployment = {
  baseUrl: (app: string) => string;
  fetch: Fetch;
};

export type CellContext = {
  fixture: FixtureSpec;
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
  legTimeoutMs: number;
  legs: Leg[];
  guard: () => Promise<ExpectationEnvironment>;
  prepare?: () => Promise<PrepareFailures | void>;
  setup: () => Promise<void>;
  up: (cell: CellContext) => Promise<Deployment>;
  redeploy?: (cell: CellContext, greeting: string) => Promise<Deployment>;
  rollback?: (cell: CellContext, greeting: string) => Promise<Deployment>;
  destroy: (cell: CellContext) => Promise<void>;
  list: () => Promise<string[]>;
  stands: (slug: string) => Promise<boolean>;
  sweep: (runId: string) => Promise<void>;
};

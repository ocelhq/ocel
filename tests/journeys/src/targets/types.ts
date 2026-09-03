import type { Evidence } from "../evidence";
import type { ExpectationEnvironment } from "../expectations/types";
import type { ExampleSpec, Leg, TargetName } from "../spec";

export type Deployment = {
  baseUrl: (app: string) => string;
  fetch: typeof fetch;
};

export type CellContext = {
  example: ExampleSpec;
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
  prepare?: () => Promise<void>;
  setup: () => Promise<void>;
  up: (cell: CellContext) => Promise<Deployment>;
  redeploy?: (cell: CellContext, greeting: string) => Promise<Deployment>;
  rollback?: (cell: CellContext, greeting: string) => Promise<Deployment>;
  destroy: (cell: CellContext) => Promise<void>;
  list: () => Promise<string[]>;
  stands: (slug: string) => Promise<boolean>;
  sweep: (runId: string) => Promise<void>;
};

import type { Evidence } from "../evidence";
import type { ExpectationEnvironment } from "../expectations/types";
import type { Compute, Edge, ExampleSpec, Leg, Mode, Suite, TargetName } from "../spec";

export type Deployment = {
  baseUrl: (app: string) => string;
  fetch: typeof fetch;
};

export type CellContext = {
  example: ExampleSpec;
  name: string;
  mode: Mode;
  compute: Compute;
  edge?: Edge;
  suites: Suite[];
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
  modes: Mode[];
  computes: Compute[];
  edges: Edge[];
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

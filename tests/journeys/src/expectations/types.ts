import type { Leg, Suite } from "../spec";

export type ExpectationEnvironment = "aws" | "aws.floci" | "dev" | "vps" | "vps.incus";

export type Edge = "api-gateway" | "cloudfront" | "cloudflare";

export type TestPick =
  | string
  | { row: string; legs?: Leg[] }
  | { rows: Suite[] | "every"; legs?: Leg[]; except?: string[] };

export type Affected = {
  on: ExpectationEnvironment[];
  edge?: Edge[];
  cells?: string[];
  tests: TestPick[];
};

export type Gap = {
  id: string;
  reason: string;
  issue?: number;
  affects: Affected[];
};

export type Listed = Pick<Gap, "id" | "reason" | "issue">;

export type Expectations = Record<string, Record<string, Listed[]>>;

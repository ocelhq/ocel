import type { Leg, Suite } from "../spec";

export type ExpectationEnvironment = "aws" | "aws.floci" | "dev" | "vps" | "vps.incus";

export const ENVIRONMENTS: ExpectationEnvironment[] = ["aws", "aws.floci", "dev", "vps", "vps.incus"];

export type TestPick =
  | string
  | { row: string; legs?: Leg[] }
  | { rows: Suite[] | "every"; legs?: Leg[]; except?: string[] };

export type Affected = {
  on: ExpectationEnvironment[];
  cells?: string[];
  variants?: string[];
  tests: TestPick[];
  skip?: true;
};

export type Gap = {
  id: string;
  reason: string;
  issue?: number;
  affects: Affected[];
};

export type Listed = Pick<Gap, "id" | "reason" | "issue">;

export type Expectations = Record<string, Record<string, Listed[]>>;

export type Skipped = Record<string, Listed[]>;

export type Resolved = { expectations: Expectations; skipped: Skipped };

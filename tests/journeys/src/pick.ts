import { createHash } from "node:crypto";
import {
  type ExampleSpec,
  modesOf,
  type Offered,
  preferredOf,
  type Variant,
  variantsOf,
} from "./spec";

export type Pick = { seed: string; touched: string[] };

export type Coverage = "full" | "covering";

export const COVERAGES: Coverage[] = ["full", "covering"];

function rotation(seed: string, group: string): number {
  return createHash("sha256").update(`${seed}:${group}`).digest().readUInt32BE(0);
}

function memberFor(seed: string, group: string, members: ExampleSpec[]): ExampleSpec {
  return members[rotation(seed, group) % members.length];
}

function untouched(seed: string, group: string, members: ExampleSpec[]): ExampleSpec {
  const preferred = members.find((row) => row.name === preferredOf(group));
  return preferred ?? memberFor(seed, group, members);
}

export function pickExamples(
  rows: ExampleSpec[],
  pick: Pick | undefined,
): { chosen: ExampleSpec[]; leftOut: ExampleSpec[] } {
  if (!pick) {
    return { chosen: rows, leftOut: [] };
  }

  const groups = new Map<string, ExampleSpec[]>();
  for (const row of rows) {
    if (row.group === undefined) {
      continue;
    }
    const members = groups.get(row.group) ?? [];
    members.push(row);
    groups.set(row.group, members);
  }

  const running = new Set<ExampleSpec>();
  for (const [group, members] of groups) {
    const touched = members.filter((row) => pick.touched.includes(row.dir));
    for (const row of touched.length > 0 ? touched : [untouched(pick.seed, group, members)]) {
      running.add(row);
    }
  }

  const chosen = rows.filter((row) => row.group === undefined || running.has(row));
  const leftOut = rows.filter((row) => row.group !== undefined && !running.has(row));
  return { chosen, leftOut };
}

export function requestedPick(env: NodeJS.ProcessEnv = process.env): Pick | undefined {
  const seed = (env.OCEL_JOURNEY_SEED ?? "").trim();
  if (seed === "") {
    return undefined;
  }
  return {
    seed,
    touched: (env.OCEL_JOURNEY_TOUCHED ?? "")
      .split(",")
      .map((dir) => dir.trim())
      .filter((dir) => dir !== ""),
  };
}

export function coverageFrom(env: NodeJS.ProcessEnv = process.env): Coverage {
  const asked = (env.OCEL_JOURNEY_COVERAGE ?? "").trim();
  if (asked === "") {
    return "covering";
  }
  if (!(COVERAGES as string[]).includes(asked)) {
    throw new Error(
      `OCEL_JOURNEY_COVERAGE is ${asked}, and a journey covers its cells ${COVERAGES.join(" or ")}`,
    );
  }
  return asked as Coverage;
}

function sameVariant(one: Variant, other: Variant): boolean {
  return one.mode === other.mode && one.compute === other.compute && one.edge === other.edge;
}

function defaultVariant(example: ExampleSpec, offered: Offered): Variant {
  const [variant] = variantsOf(example, offered);
  if (!variant) {
    throw new Error(`${example.name} runs in no variant this target offers`);
  }
  return variant;
}

function oneFactorOff(offered: Offered): Variant[] {
  const mode = offered.modes[0];
  const compute = offered.computes[0];
  const edge = offered.edges[0];
  const rest: Variant = edge === undefined ? { mode, compute } : { mode, compute, edge };
  return [
    ...offered.modes.slice(1).map((one) => ({ ...rest, mode: one })),
    ...offered.computes.slice(1).map((one) => ({ ...rest, compute: one })),
    ...offered.edges.slice(1).map((one) => ({ ...rest, edge: one })),
  ];
}

function foldedIntoAModeItRuns(member: ExampleSpec, variant: Variant, offered: Offered): Variant {
  const modes = modesOf(member, offered.modes);
  return modes.includes(variant.mode) ? variant : { ...variant, mode: modes[0] };
}

function takerOf(
  free: ExampleSpec[],
  variant: Variant,
  offered: Offered,
  turn: number,
): ExampleSpec {
  const able = free.filter((row) => modesOf(row, offered.modes).includes(variant.mode));
  const pool = able.length > 0 ? able : free;
  return pool[turn % pool.length];
}

function coverGroup(
  group: string,
  members: ExampleSpec[],
  offered: Offered,
  pick: Pick,
): Map<string, Variant[]> {
  const chosen = new Map<string, Variant[]>();
  const add = (example: ExampleSpec, variant: Variant) => {
    const held = chosen.get(example.name) ?? [];
    if (!held.some((one) => sameVariant(one, variant))) {
      held.push(variant);
    }
    chosen.set(example.name, held);
  };

  const free: ExampleSpec[] = [];
  for (const member of members) {
    if (pick.touched.includes(member.dir)) {
      chosen.set(member.name, variantsOf(member, offered));
    } else {
      free.push(member);
    }
  }
  if (free.length === 0) {
    return chosen;
  }

  const start = rotation(pick.seed, group);
  const lead = free.find((row) => row.name === preferredOf(group)) ?? free[start % free.length];
  add(lead, defaultVariant(lead, offered));

  for (const [index, variant] of oneFactorOff(offered).entries()) {
    const member = takerOf(free, variant, offered, start + index);
    add(member, foldedIntoAModeItRuns(member, variant, offered));
  }

  for (const member of free) {
    if (!chosen.has(member.name)) {
      add(member, defaultVariant(member, offered));
    }
  }
  return chosen;
}

export function coverVariants(
  rows: ExampleSpec[],
  offered: Offered,
  coverage: Coverage,
  pick: Pick | undefined,
): Map<string, Variant[]> {
  if (coverage === "full") {
    return new Map(rows.map((row) => [row.name, variantsOf(row, offered)]));
  }
  const asked = pick ?? { seed: "", touched: [] };
  const groups = new Map<string, ExampleSpec[]>();
  for (const row of rows) {
    const name = row.group ?? row.name;
    groups.set(name, [...(groups.get(name) ?? []), row]);
  }
  const out = new Map<string, Variant[]>();
  for (const [group, members] of groups) {
    for (const [name, variants] of coverGroup(group, members, offered, asked)) {
      out.set(name, variants);
    }
  }
  return new Map(rows.map((row) => [row.name, out.get(row.name) ?? []]));
}

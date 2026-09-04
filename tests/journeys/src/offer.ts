import type { Compute, Edge, Offered } from "./spec";

export type Offering = Offered & { name: string };

function narrowed<T extends string>(
  offered: T[],
  asked: string | undefined,
  axis: string,
  target: string,
): T[] {
  const named = (asked ?? "").split(/[\s,]+/).filter((name) => name !== "");
  if (named.length === 0) {
    return offered;
  }
  const unknown = named.filter((name) => !(offered as string[]).includes(name));
  if (unknown.length > 0) {
    throw new Error(
      `the ${target} target offers no ${axis} named ${unknown.join(", ")} (${offered.join(", ")})`,
    );
  }
  return offered.filter((one) => named.includes(one));
}

export function offeredBy(target: Offering, env: NodeJS.ProcessEnv = process.env): Offered {
  return {
    modes: target.modes,
    computes: narrowed<Compute>(target.computes, env.OCEL_JOURNEY_COMPUTES, "compute", target.name),
    edges: narrowed<Edge>(target.edges, env.OCEL_JOURNEY_EDGES, "edge", target.name),
  };
}

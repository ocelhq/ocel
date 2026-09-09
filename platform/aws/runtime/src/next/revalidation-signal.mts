export const revalidatedHeader = "x-ocel-revalidated";

interface Signal {
  ticks: number;
}

const stateKey = Symbol.for("ocel.next.revalidation-signal.v1");

function signal(): Signal {
  const host = globalThis as Record<symbol, Signal | undefined>;
  return (host[stateKey] ??= { ticks: 0 });
}

export function noteRevalidation(): void {
  signal().ticks++;
}

export function revalidationTicks(): number {
  return signal().ticks;
}

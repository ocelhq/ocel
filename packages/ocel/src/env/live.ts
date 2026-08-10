const LIVE_VALUES = Symbol.for("ocel.env.liveValues");

interface LiveValues {
  generation: number;
  values: Record<string, string>;
}

export const NO_GENERATION = 0;

function published(): Partial<LiveValues> | undefined {
  return (globalThis as Record<symbol, unknown>)[LIVE_VALUES] as
    | Partial<LiveValues>
    | undefined;
}

export function liveGeneration(): number {
  return published()?.generation ?? NO_GENERATION;
}

export function readLive(key: string): string | undefined {
  const value = published()?.values?.[key];
  return typeof value === "string" ? value : undefined;
}

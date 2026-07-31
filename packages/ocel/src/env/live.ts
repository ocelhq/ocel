// A live value is the one class that never rides in an artifact and never
// reaches the process environment: the membrane fetches it from the store while
// this process is starting and pushes it in, and pushes it again whenever it
// refreshes. This module is the whole of what the application process holds —
// a map, and which push it came from.
//
// It reads a well-known global rather than a module binding because the
// receiver and the reader can never share a module instance: the entrypoint
// that takes the push off the control socket ships in the Lambda layer, and
// this file ships inside the application's own bundle. The global is the seam,
// and it carries plain data only, so the two sides agree on a shape rather than
// on code. The receiver owns writing it, including refusing an out-of-order
// push; nothing here writes.
const LIVE_VALUES = Symbol.for("ocel.env.liveValues");

interface LiveValues {
  generation: number;
  values: Record<string, string>;
}

// NO_GENERATION is the absence of a push. It is a real state, not an error: a
// function that declares nothing live is never pushed to, and neither is one
// running under `ocel dev`, which has no membrane and delivers every class
// through the environment.
export const NO_GENERATION = 0;

function published(): Partial<LiveValues> | undefined {
  return (globalThis as Record<symbol, unknown>)[LIVE_VALUES] as
    | Partial<LiveValues>
    | undefined;
}

// liveGeneration is which push the values on offer came from. It is what a
// memoised live value is pinned to: the same generation is the same value, and
// a later one is the rotation the memo has to notice.
export function liveGeneration(): number {
  return published()?.generation ?? NO_GENERATION;
}

// readLive answers only from a push. Falling back is the caller's business,
// because the fallback is the ordinary environment read every class shares.
//
// Only a string is a value. The global is reachable by anything in the process,
// and handing a schema promised a string something else instead would turn a
// mistake elsewhere into a variable this key appears to hold.
export function readLive(key: string): string | undefined {
  const value = published()?.values?.[key];
  return typeof value === "string" ? value : undefined;
}

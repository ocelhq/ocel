import { onControlMessage } from "./membrane.mjs";

// A live-class value is never in the bundle and never in this process's
// environment: the membrane fetches it from the store while this process is
// starting and pushes it down the control socket, then pushes again on every
// refresh. This module is the receiving half.
//
// What it receives is published on a well-known global rather than exported,
// because the code that reads it — the `ocel/env` SDK — ships inside the
// application's own bundle and this file ships in the layer. They are separate
// module graphs and can never share an instance, so the seam between them is
// plain data under an agreed symbol.

// LIVE_VALUES_MESSAGE is the one message type the membrane sends this way. It
// is one-way and fire-and-forget: nothing here answers it, and a refresh that
// failed sends nothing at all, which is what leaves the last generation being
// served while the membrane retries.
const LIVE_VALUES_MESSAGE = "liveValues";

const LIVE_VALUES = Symbol.for("ocel.env.liveValues");

// LIVE_KEYS_ENV_VAR names the live-class keys this function declares, pinned at
// deploy from the same manifest the membrane addresses the store by. It carries
// bare key names and nothing else — no folder, no coordinate, no value — so the
// runtime stays as folder-blind as it is for every other class.
//
// Its absence is the whole of "this function declares nothing live": no wait at
// boot, no subscription, and no store call was ever going to be made on its
// behalf, so a store outage cannot reach it.
export const LIVE_KEYS_ENV_VAR = "OCEL_LIVE_KEYS";

interface LiveValues {
  generation: number;
  values: Record<string, string>;
}

export function declaredLiveKeys(): string[] {
  return (process.env[LIVE_KEYS_ENV_VAR] ?? "")
    .split(",")
    .map((key) => key.trim())
    .filter((key) => key !== "");
}

function published(): LiveValues | undefined {
  return (globalThis as Record<symbol, unknown>)[LIVE_VALUES] as
    | LiveValues
    | undefined;
}

// parseLiveValues accepts only what the contract describes. A generation is a
// whole number from one upward, and every value is a string; anything else is
// not a push this side knows how to serve, and guessing at it would put a value
// of an unknown shape in front of a schema that expects a string.
function parseLiveValues(message: unknown): LiveValues | undefined {
  if (typeof message !== "object" || message === null) return undefined;
  const { type, generation, values } = message as Record<string, unknown>;
  if (type !== LIVE_VALUES_MESSAGE) return undefined;
  if (typeof generation !== "number" || !Number.isInteger(generation)) return undefined;
  if (generation < 1) return undefined;
  if (typeof values !== "object" || values === null) return undefined;

  const entries = Object.entries(values);
  if (entries.some(([, value]) => typeof value !== "string")) return undefined;
  return { generation, values: Object.fromEntries(entries) as Record<string, string> };
}

// applyLiveValues installs a push and reports whether it changed anything. A
// generation no greater than the one already installed is dropped: pushes are
// fire-and-forget, so a refresh that overtook a later one on the way here would
// otherwise resurrect a value the membrane has already replaced — for as long
// as this sandbox lived, which no staleness bound could cover.
export function applyLiveValues(message: unknown): boolean {
  const pushed = parseLiveValues(message);
  if (!pushed) return false;

  const current = published();
  if (current && pushed.generation <= current.generation) return false;

  (globalThis as Record<symbol, unknown>)[LIVE_VALUES] = pushed;
  return true;
}

let firstPush: Promise<void> | undefined;

// awaitLiveValues holds the boot until the first push has landed, and only for
// a function that declares live values. Waiting here rather than at the read is
// what keeps a read a plain synchronous property access: by the time any
// application code runs — including a read written at module scope, which runs
// the instant its file is imported — the values are already in hand.
//
// The wait is deliberately unbounded. The membrane's own startup budget bounds
// it: a node process that never becomes ready fails init there, with the reason
// the membrane already knows, and a second timeout here could only invent a
// worse one.
export function awaitLiveValues(): Promise<void> {
  if (declaredLiveKeys().length === 0) return Promise.resolve();

  firstPush ??= new Promise<void>((resolve) => {
    onControlMessage((message) => {
      // The same subscription serves every later refresh; resolving again is
      // what a settled promise already ignores.
      if (applyLiveValues(message)) resolve();
    });
  });
  return firstPush;
}

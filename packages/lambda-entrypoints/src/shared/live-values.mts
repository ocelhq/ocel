import { onControlMessage } from "./membrane.mjs";

const LIVE_VALUES_MESSAGE = "liveValues";

const LIVE_VALUES = Symbol.for("ocel.env.liveValues");

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

export function applyLiveValues(message: unknown): boolean {
  const pushed = parseLiveValues(message);
  if (!pushed) return false;

  const current = published();
  if (current && pushed.generation <= current.generation) return false;

  (globalThis as Record<symbol, unknown>)[LIVE_VALUES] = pushed;
  return true;
}

let firstPush: Promise<void> | undefined;

export function awaitLiveValues(): Promise<void> {
  if (declaredLiveKeys().length === 0) return Promise.resolve();

  firstPush ??= new Promise<void>((resolve) => {
    onControlMessage((message) => {
      if (applyLiveValues(message)) resolve();
    });
  });
  return firstPush;
}

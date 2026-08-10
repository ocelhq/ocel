import { Abort } from "./abort.ts";
import { tokenizeFlags } from "./options.ts";
import type { KeyScope } from "./race.ts";

export interface RaceOptions {
  base: string;
  phase: "control" | "gap" | "burst";
  trials: number;
  deltas: number[];
  sizes: number[];
  jitters: number[];
  sockets: number;
  scope: KeyScope;
  windowMs: number | null;
  isolatesPerColo: number | null;
  colos: number;
  sentinelTtlSeconds: number;
  maxDiscardRate: number;
  out: string;
}

const DEFAULT_DELTAS = [0, 10, 25, 50, 100, 150, 200, 300, 500, 1_000];
const DEFAULT_SIZES = [2, 8, 32, 128];

export function parseRaceOptions(argv: string[], defaultOut: string): RaceOptions {
  const flags = tokenizeFlags(argv, (message) => new Abort(message));

  const base = flags.get("base");
  if (!base) throw new Abort("--base <https://probe.example.com> is required");
  const phase = flags.get("phase") ?? "control";
  if (phase !== "control" && phase !== "gap" && phase !== "burst") {
    throw new Abort(`--phase must be control, gap or burst, got "${phase}"`);
  }
  const scope = flags.get("scope") ?? "offzone";
  if (scope !== "onzone" && scope !== "offzone") {
    throw new Abort(`--scope must be onzone or offzone, got "${scope}"`);
  }

  const finite = (name: string, raw: string) => {
    const parsed = Number(raw);
    if (raw.trim() === "" || !Number.isFinite(parsed)) {
      throw new Abort(`--${name} must be a number, got "${raw}"`);
    }
    return parsed;
  };
  const atLeast = (name: string, raw: string, floor: number) => {
    const parsed = finite(name, raw);
    if (parsed < floor) throw new Abort(`--${name} must be at least ${floor}, got "${raw}"`);
    return parsed;
  };
  const within = (name: string, raw: string, floor: number, ceiling: number) => {
    const parsed = atLeast(name, raw, floor);
    if (parsed > ceiling) {
      throw new Abort(`--${name} must be between ${floor} and ${ceiling}, got "${raw}"`);
    }
    return parsed;
  };
  const above = (name: string, raw: string, floor: number) => {
    const parsed = finite(name, raw);
    if (!(parsed > floor)) {
      throw new Abort(`--${name} must be greater than ${floor}, got "${raw}"`);
    }
    return parsed;
  };
  const wholeAtLeast = (name: string, raw: string, floor: number) => {
    const parsed = atLeast(name, raw, floor);
    if (!Number.isInteger(parsed)) {
      throw new Abort(`--${name} must be a whole number, got "${raw}"`);
    }
    return parsed;
  };
  const of = <T>(name: string, fallback: T, read: (raw: string) => T): T => {
    const raw = flags.get(name);
    return raw === undefined ? fallback : read(raw);
  };
  const listOf = (name: string, fallback: number[], read: (raw: string) => number) =>
    of(name, fallback, (raw) => raw.split(",").map(read));

  return {
    base: base.replace(/\/$/, ""),
    phase,
    scope,
    trials: of("trials", phase === "gap" ? 200 : 100, (raw) => wholeAtLeast("trials", raw, 1)),
    deltas: listOf("deltas", DEFAULT_DELTAS, (raw) => atLeast("deltas", raw, 0)),
    sizes: listOf("sizes", DEFAULT_SIZES, (raw) => wholeAtLeast("sizes", raw, 1)),
    jitters: listOf("jitters", [0], (raw) => atLeast("jitters", raw, 0)),
    sockets: of("sockets", 16, (raw) => wholeAtLeast("sockets", raw, 2)),
    windowMs: of("window", null, (raw) => atLeast("window", raw, 0)),
    isolatesPerColo: of("isolates", null, (raw) => wholeAtLeast("isolates", raw, 1)),
    colos: of("colos", 300, (raw) => wholeAtLeast("colos", raw, 1)),
    sentinelTtlSeconds: of("sentinelTtl", 5, (raw) => above("sentinelTtl", raw, 0)),
    maxDiscardRate: of("maxDiscardRate", 0.02, (raw) => within("maxDiscardRate", raw, 0, 1)),
    out: flags.get("out") ?? defaultOut,
  };
}

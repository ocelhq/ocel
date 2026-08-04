export interface Options {
  base: string;
  concurrency: number;
  rounds: number;
  sentinelSeconds: number;
  ttls: number[];
  pollSeconds: number;
  pollFanout: number;
  windowSeconds: number;
  out: string;
}

type Numeric = Exclude<keyof Options, "base" | "out" | "ttls">;

const defaults: Record<Numeric, number> & { ttls: number[] } = {
  concurrency: 64,
  rounds: 4,
  sentinelSeconds: 60,
  ttls: [10],
  pollSeconds: 1,
  pollFanout: 8,
  windowSeconds: 180,
};

// Shared with race-options.ts so the two runners cannot drift on what an
// argument vector means. The failure type is a parameter because each runner
// aborts through its own error class.
export function tokenizeFlags(
  argv: string[],
  fail: (message: string) => Error,
): Map<string, string> {
  const flags = new Map<string, string>();
  for (let i = 0; i < argv.length; i += 2) {
    const flag = argv[i];
    if (!flag?.startsWith("--")) throw fail(`unexpected argument: ${flag}`);
    const value = argv[i + 1];
    if (value === undefined || value.startsWith("--")) {
      throw fail(`${flag} requires a value`);
    }
    flags.set(flag.slice(2), value);
  }
  return flags;
}

export function parseArgs(argv: string[], defaultOut: string): Options {
  const flags = tokenizeFlags(argv, (message) => new Error(message));

  const base = flags.get("base");
  if (!base) throw new Error("--base <https://probe.example.com> is required");

  const positive = (name: string, raw: string) => {
    const parsed = Number(raw);
    if (!Number.isFinite(parsed) || parsed <= 0) {
      throw new Error(`--${name} must be a positive number, got "${raw}"`);
    }
    return parsed;
  };
  const number = (name: Numeric) => {
    const raw = flags.get(name);
    return raw === undefined ? defaults[name] : positive(name, raw);
  };

  const rawTtls = flags.get("ttls");
  const ttls =
    rawTtls === undefined
      ? defaults.ttls
      : rawTtls.split(",").map((ttl) => positive("ttls", ttl));

  return {
    base: base.replace(/\/$/, ""),
    concurrency: number("concurrency"),
    rounds: number("rounds"),
    sentinelSeconds: number("sentinelSeconds"),
    pollSeconds: number("pollSeconds"),
    pollFanout: number("pollFanout"),
    windowSeconds: number("windowSeconds"),
    ttls,
    out: flags.get("out") ?? defaultOut,
  };
}

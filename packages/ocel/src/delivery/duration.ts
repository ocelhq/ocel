import { type Timestamp, timestampFromMs } from "@bufbuild/protobuf/wkt";

/**
 * A length of time: a number of seconds, or a string such as `"500ms"`, `"30s"`, `"5m"`,
 * `"2h"` or `"1d"`.
 */
export type Duration =
  | number
  | `${number}ms`
  | `${number}s`
  | `${number}m`
  | `${number}h`
  | `${number}d`;

const millisecondsPerUnit = {
  ms: 1,
  s: 1_000,
  m: 60_000,
  h: 3_600_000,
  d: 86_400_000,
} as const;

const durationPattern = /^(\d+(?:\.\d+)?)(ms|s|m|h|d)$/;

export function parseDurationMilliseconds(duration: Duration): number {
  if (typeof duration === "number") {
    if (!Number.isFinite(duration) || duration < 0) {
      throw new Error(`a duration is a non-negative number of seconds, not ${duration}`);
    }
    return duration * 1_000;
  }
  const match = durationPattern.exec(duration);
  if (!match?.[1] || !match[2]) {
    throw new Error(
      `"${duration}" is not a duration: write a number of seconds, or a string such as "500ms", "30s", "5m", "2h" or "1d"`,
    );
  }
  return Number(match[1]) * millisecondsPerUnit[match[2] as keyof typeof millisecondsPerUnit];
}

export interface DurationFields {
  seconds: bigint;
  nanos: number;
}

export function encodeDuration(duration: Duration | undefined): DurationFields | undefined {
  if (duration === undefined) return undefined;
  const milliseconds = Math.round(parseDurationMilliseconds(duration));
  return {
    seconds: BigInt(Math.floor(milliseconds / 1_000)),
    nanos: (milliseconds % 1_000) * 1_000_000,
  };
}

export function encodeDueAt(delay: Duration | Date | undefined): Timestamp | undefined {
  if (delay === undefined) return undefined;
  return timestampFromMs(
    delay instanceof Date ? delay.getTime() : Date.now() + parseDurationMilliseconds(delay),
  );
}

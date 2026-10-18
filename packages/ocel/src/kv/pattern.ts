type Segment = { literal: string } | { parameter: string };

export interface Pattern {
  readonly written: string;
  readonly segments: readonly Segment[];
}

/** The parameter names a pattern such as `"rooms/:room/members/:member"` holds. */
export type PatternParameters<TPattern extends string> =
  TPattern extends `${infer Head}/${infer Rest}`
    ? SegmentParameter<Head> | PatternParameters<Rest>
    : SegmentParameter<TPattern>;

type SegmentParameter<TSegment extends string> = TSegment extends `:${infer Name}` ? Name : never;

/**
 * The key of an entry: one value for each parameter of its pattern, a string or a safe
 * integer. A number that is not a safe integer is refused when the key is built.
 */
export type KeyOf<TPattern extends string> = {
  [Name in PatternParameters<TPattern>]: string | number;
};

/** The leading argument an entry's operations take: its key, or nothing for a pattern without parameters. */
export type KeyArgs<TPattern extends string> = [PatternParameters<TPattern>] extends [never]
  ? []
  : [key: KeyOf<TPattern>];

export function parsePattern(written: string): Pattern {
  const segments = written
    .split("/")
    .map(
      (part): Segment => (part.startsWith(":") ? { parameter: part.slice(1) } : { literal: part }),
    );
  return { written, segments };
}

export function hasParameters(pattern: Pattern): boolean {
  return pattern.segments.some((segment) => "parameter" in segment);
}

const unreserved = /[A-Za-z0-9\-._~]/;

export function encodeParameter(value: string): string {
  let encoded = "";
  for (const byte of new TextEncoder().encode(value)) {
    const char = String.fromCharCode(byte);
    encoded +=
      byte < 0x80 && unreserved.test(char)
        ? char
        : `%${byte.toString(16).toUpperCase().padStart(2, "0")}`;
  }
  return encoded;
}

export function buildKey(pattern: Pattern, key: Record<string, unknown> | undefined): string {
  return pattern.segments
    .map((segment) => {
      if ("literal" in segment) return segment.literal;
      const value = key?.[segment.parameter];
      if (typeof value === "number" && !Number.isSafeInteger(value)) {
        throw new TypeError(
          `the key of pattern "${pattern.written}" gives :${segment.parameter} the number ${value}, which is no safe integer: give a string or an integer within ±(2^53 - 1)`,
        );
      }
      if (typeof value !== "string" && typeof value !== "number") {
        throw new TypeError(
          `the key of pattern "${pattern.written}" has no value for :${segment.parameter}: give a string or an integer`,
        );
      }
      return encodeParameter(String(value));
    })
    .join("/");
}

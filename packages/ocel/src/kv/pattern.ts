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

const literalSegment = /^[A-Za-z0-9._-]+$/;
const parameterSegment = /^:[A-Za-z_][A-Za-z0-9_]*$/;

export function parsePattern(written: string): Pattern {
  if (written === "") {
    throw new Error(
      "the pattern is empty: write one or more segments joined by /, each a literal or a :parameter",
    );
  }
  if (/[{}]/.test(written)) {
    throw new Error(
      `pattern "${written}" holds { or }, which a store reserves as the hash tag syntax`,
    );
  }
  const segments: Segment[] = [];
  const named = new Set<string>();
  for (const part of written.split("/")) {
    if (part === "") {
      throw new Error(
        `pattern "${written}" has an empty segment: it neither starts nor ends with /, and no two / are adjacent`,
      );
    }
    if (part.startsWith(":")) {
      if (!parameterSegment.test(part)) {
        throw new Error(
          `pattern "${written}" has segment "${part}", which is no parameter: a parameter is : and a name of letters, digits and _ that starts with a letter or _`,
        );
      }
      const name = part.slice(1);
      if (named.has(name)) {
        throw new Error(
          `pattern "${written}" names parameter "${name}" twice, so a key could not say which value is which`,
        );
      }
      named.add(name);
      segments.push({ parameter: name });
      continue;
    }
    if (!literalSegment.test(part)) {
      throw new Error(
        `pattern "${written}" has segment "${part}", which is no literal: a literal is letters, digits, ., _ and -`,
      );
    }
    segments.push({ literal: part });
  }
  return { written, segments };
}

export function patternsOverlap(a: Pattern, b: Pattern): boolean {
  if (a.segments.length !== b.segments.length) return false;
  return a.segments.every((mine, i) => {
    const theirs = b.segments[i] as Segment;
    return !("literal" in mine && "literal" in theirs) || mine.literal === theirs.literal;
  });
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

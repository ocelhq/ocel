type Segment = { literal: string } | { parameter: string };

/** A channel pattern as written, split into its literal and `:parameter` segments. */
export interface ChannelPattern {
  readonly written: string;
  readonly segments: readonly Segment[];
}

/** Why params cannot fill a pattern into a wire channel. */
export type WireRefusal = "missing-param" | "unknown-param" | "empty-value" | "value-too-long";

/** The wire channel params fill a pattern into, or why they cannot. */
export type WireChannel = { channel: string } | { refused: WireRefusal };

const maxSegments = 4;
const maxValueBytes = 30;
const channelSegment = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,48}[A-Za-z0-9])?$/;
const parameterSegment = /^:[A-Za-z_][A-Za-z0-9_]*$/;
const segmentRule =
  "letters, digits and -, at most 50 characters, starting and ending with a letter or digit";

/** Parses a written channel pattern, throwing for one its grammar does not allow. */
export function parseChannelPattern(written: string): ChannelPattern {
  if (written === "") {
    throw new Error(
      `the pattern is empty: write one to ${maxSegments} segments joined by /, each a literal or a :parameter`,
    );
  }
  const parts = written.split("/");
  if (parts.length > maxSegments) {
    throw new Error(
      `pattern "${written}" has ${parts.length} segments, and a channel pattern has at most ${maxSegments}`,
    );
  }
  const segments: Segment[] = [];
  const named = new Set<string>();
  for (const part of parts) {
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
          `pattern "${written}" names parameter "${name}" twice, so a channel could not say which value is which`,
        );
      }
      named.add(name);
      segments.push({ parameter: name });
    } else if (!channelSegment.test(part)) {
      throw new Error(
        `pattern "${written}" has segment "${part}", which is no literal: a literal is ${segmentRule}`,
      );
    } else {
      segments.push({ literal: part });
    }
  }
  return { written, segments };
}

/** Whether `name` can begin every channel of a realtime resource. */
export function isChannelNamespace(name: string): boolean {
  return channelSegment.test(name);
}

/** The names of the `:parameter` segments of `pattern`, in order. */
export function listPatternParameters(pattern: ChannelPattern): string[] {
  return pattern.segments.flatMap((s) => ("parameter" in s ? [s.parameter] : []));
}

const base32Alphabet = "abcdefghijklmnopqrstuvwxyz234567";

function encodeLowerBase32(bytes: Uint8Array): string {
  let out = "";
  let buffer = 0;
  let bits = 0;
  for (const byte of bytes) {
    buffer = (buffer << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      bits -= 5;
      out += base32Alphabet[(buffer >> bits) & 31];
    }
  }
  if (bits > 0) out += base32Alphabet[(buffer << (5 - bits)) & 31];
  return out;
}

function encodeValue(value: string, bytes: Uint8Array): string {
  if (channelSegment.test(value) && !value.startsWith("0z")) return value;
  return `0z${encodeLowerBase32(bytes)}`;
}

/**
 * Encodes the wire channel `params` fill `pattern` into under `namespace`; a `wildcard`
 * pattern may leave off trailing params and ends in `*`.
 */
export function encodeWireChannel(
  namespace: string,
  pattern: ChannelPattern,
  params: Record<string, string>,
  wildcard: boolean,
): WireChannel {
  const names = listPatternParameters(pattern);
  for (const name of Object.keys(params)) {
    if (!names.includes(name)) return { refused: "unknown-param" };
  }
  const channel = ["", namespace];
  for (const [i, segment] of pattern.segments.entries()) {
    if ("literal" in segment) {
      channel.push(segment.literal);
      continue;
    }
    const value = Object.hasOwn(params, segment.parameter) ? params[segment.parameter] : undefined;
    if (value === undefined) {
      const later = pattern.segments
        .slice(i)
        .some((s) => "parameter" in s && Object.hasOwn(params, s.parameter));
      if (!wildcard || later) return { refused: "missing-param" };
      return { channel: [...channel, "*"].join("/") };
    }
    if (value === "") return { refused: "empty-value" };
    const bytes = new TextEncoder().encode(value);
    if (bytes.byteLength > maxValueBytes) return { refused: "value-too-long" };
    channel.push(encodeValue(value, bytes));
  }
  return { channel: channel.join("/") };
}

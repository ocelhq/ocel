import { fromJson, type JsonValue } from "@bufbuild/protobuf";
import { JsonText } from "../delivery/json-text.js";
import { type Envelope, EnvelopeSchema } from "../gen/proto/app/topic/v1/topic_pb.js";

export interface DeliveredPayload {
  value: unknown;
  json: JsonText;
}

export interface ParsedEnvelope {
  envelope: Envelope;
  payloads: DeliveredPayload[];
}

const WHITESPACE = new Set([" ", "\t", "\n", "\r"]);
const VALUE_END = new Set([",", "}", "]", ...WHITESPACE]);

function skipWhitespace(text: string, from: number): number {
  let i = from;
  while (i < text.length && WHITESPACE.has(text.charAt(i))) i++;
  return i;
}

function skipString(text: string, from: number): number {
  let i = from + 1;
  while (text.charAt(i) !== '"') i += text.charAt(i) === "\\" ? 2 : 1;
  return i + 1;
}

function skipValue(text: string, from: number): number {
  const first = text.charAt(from);
  if (first === '"') return skipString(text, from);
  if (first !== "{" && first !== "[") {
    let i = from;
    while (i < text.length && !VALUE_END.has(text.charAt(i))) i++;
    return i;
  }
  let depth = 0;
  let i = from;
  while (i < text.length) {
    const char = text.charAt(i);
    if (char === '"') {
      i = skipString(text, i);
      continue;
    }
    if (char === "{" || char === "[") depth++;
    if (char === "}" || char === "]") depth--;
    i++;
    if (depth === 0) break;
  }
  return i;
}

function readMemberTexts(objectText: string): Map<string, string> {
  const members = new Map<string, string>();
  let i = skipWhitespace(objectText, skipWhitespace(objectText, 0) + 1);
  while (objectText.charAt(i) === '"') {
    const keyEnd = skipString(objectText, i);
    const key = JSON.parse(objectText.slice(i, keyEnd)) as string;
    const valueStart = skipWhitespace(objectText, skipWhitespace(objectText, keyEnd) + 1);
    const valueEnd = skipValue(objectText, valueStart);
    members.set(key, objectText.slice(valueStart, valueEnd));
    i = skipWhitespace(objectText, valueEnd);
    if (objectText.charAt(i) === ",") i = skipWhitespace(objectText, i + 1);
  }
  return members;
}

function readElementTexts(arrayText: string): string[] {
  const elements: string[] = [];
  let i = skipWhitespace(arrayText, skipWhitespace(arrayText, 0) + 1);
  while (i < arrayText.length && arrayText.charAt(i) !== "]") {
    const end = skipValue(arrayText, i);
    elements.push(arrayText.slice(i, end));
    i = skipWhitespace(arrayText, end);
    if (arrayText.charAt(i) === ",") i = skipWhitespace(arrayText, i + 1);
  }
  return elements;
}

function readPayload(members: Map<string, string>): DeliveredPayload {
  const json = new JsonText(members.get("payload") ?? "null");
  return { value: JSON.parse(json.text), json };
}

export function parseEnvelope(body: string): ParsedEnvelope {
  const parsed: unknown = JSON.parse(body);
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    throw new Error("the envelope is not a JSON object");
  }
  const { payload: _payload, messages, ...fields } = parsed as Record<string, unknown>;
  const members = readMemberTexts(body);
  const deliveries = Array.isArray(messages) ? (messages as Record<string, unknown>[]) : [];
  const bare = Array.isArray(messages)
    ? deliveries.map(({ payload: _deliveryPayload, ...delivery }) => delivery)
    : messages;
  const envelope = fromJson(
    EnvelopeSchema,
    (bare === undefined ? fields : { ...fields, messages: bare }) as JsonValue,
    { ignoreUnknownFields: true },
  );
  const payloads =
    deliveries.length > 0
      ? readElementTexts(members.get("messages") ?? "[]").map((text) =>
          readPayload(readMemberTexts(text)),
        )
      : [readPayload(members)];
  return { envelope, payloads };
}

export const EXACT_JSON = '{"ratio":2.0,"id":9007199254740993,"count":2}';

type RawJSON = { rawJSON(text: string): unknown };

type SourceReviver = (key: string, value: unknown, context: { source: string }) => unknown;

export function parseKeepingNumberText(text: string): unknown {
  const keepSource: SourceReviver = (_key, value, { source }) =>
    typeof value === "number" ? (JSON as unknown as RawJSON).rawJSON(source) : value;
  return JSON.parse(text, keepSource as Parameters<typeof JSON.parse>[1]);
}

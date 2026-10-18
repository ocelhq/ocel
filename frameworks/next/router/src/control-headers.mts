import { edgeCacheHeader } from "@platform/edge-contract/edge-cache";

export const ENTRY_HEADER = "x-ocel-entry";

const CONTROL_HEADERS = [ENTRY_HEADER, "next-resume", edgeCacheHeader];

export function withoutControlHeaders(headers: Headers): Headers {
  const kept = new Headers(headers);
  for (const name of [...headers.keys()]) {
    if (CONTROL_HEADERS.includes(name.toLowerCase())) kept.delete(name);
  }
  return kept;
}

const NEXT_MIDDLEWARE_PREFIX = "x-middleware-";

const CLIENT_CONTROL_HEADERS = [
  ...CONTROL_HEADERS,
  "x-now-route-matches",
  "x-matched-path",
  "x-nextjs-data",
  "x-next-resume-state-length",
];

export function withoutClientControl(headers: Headers): Headers {
  const kept = new Headers(headers);
  for (const name of [...headers.keys()]) {
    const lower = name.toLowerCase();
    if (lower.startsWith(NEXT_MIDDLEWARE_PREFIX) || CLIENT_CONTROL_HEADERS.includes(lower)) {
      kept.delete(name);
    }
  }
  return kept;
}

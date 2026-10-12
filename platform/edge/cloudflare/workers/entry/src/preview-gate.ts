import {
  checkPreview,
  PREVIEW_GATE_CACHE_CONTROL,
  PREVIEW_ROBOTS_TAG,
  type PreviewGate,
  parsePreviewGate,
} from "@platform/edge-contract/preview-gate";
import type { Env } from "./env";

export type PreviewGateRead =
  | { kind: "open" }
  | { kind: "gated"; gate: PreviewGate }
  | { kind: "unreadable" };

export type PreviewGateOutcome =
  | { kind: "respond"; response: Response }
  | { kind: "forward"; request: Request; responseHeaders: Headers };

export function readPreviewGate(
  env: Pick<Env, "OCEL_PREVIEW_GATE" | "OCEL_PREVIEW_GATES">,
  slug: string,
): PreviewGateRead {
  const own = env.OCEL_PREVIEW_GATE;
  if (own !== undefined) return toRead(parsePreviewGate(parseJson(own)));
  const shared = env.OCEL_PREVIEW_GATES;
  if (shared === undefined) return { kind: "open" };
  const recorded = parseJson(shared);
  if (!isRecord(recorded)) return { kind: "unreadable" };
  if (!Object.hasOwn(recorded, slug)) return { kind: "open" };
  return toRead(parsePreviewGate(recorded[slug]));
}

export async function gatePreview(
  request: Request,
  gate: PreviewGate,
  nowSeconds: number,
): Promise<PreviewGateOutcome> {
  const url = new URL(request.url);
  const target = url.pathname + url.search;
  const verdict = await checkPreview(
    gate,
    { method: request.method, host: url.host, target, headers: request.headers },
    nowSeconds,
  );
  if (verdict.kind === "respond") {
    const { status, headers, body: text } = verdict.response;
    return { kind: "respond", response: new Response(text, { status, headers }) };
  }
  return {
    kind: "forward",
    request: new Request(request, { headers: verdict.headers }),
    responseHeaders: verdict.responseHeaders,
  };
}

export function withResponseHeaders(response: Response, extra: Headers): Response {
  const marked = new Response(response.body, response);
  for (const [name, value] of extra) marked.headers.set(name, value);
  return marked;
}

export function withRobotsTag(response: Response): Response {
  return withResponseHeaders(response, new Headers({ "x-robots-tag": PREVIEW_ROBOTS_TAG }));
}

export function previewGateUnreadableResponse(): Response {
  return new Response("Service temporarily unavailable.", {
    status: 503,
    headers: {
      "content-type": "text/plain; charset=utf-8",
      "cache-control": PREVIEW_GATE_CACHE_CONTROL,
      "x-robots-tag": PREVIEW_ROBOTS_TAG,
    },
  });
}

function toRead(gate: PreviewGate | undefined): PreviewGateRead {
  return gate ? { kind: "gated", gate } : { kind: "unreadable" };
}

function parseJson(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

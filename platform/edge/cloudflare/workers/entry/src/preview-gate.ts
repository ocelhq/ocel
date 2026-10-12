import {
  checkPreview,
  PREVIEW_GATE_CACHE_CONTROL,
  PREVIEW_ROBOTS_TAG,
  type PreviewGate,
  parsePreviewGate,
} from "@platform/edge-contract/preview-gate";
import type { ReleasesBinding } from "./releases";

export type PreviewGateRead =
  | { kind: "open" }
  | { kind: "gated"; gate: PreviewGate }
  | { kind: "unreadable" };

export type PreviewGateOutcome =
  | { kind: "respond"; response: Response }
  | { kind: "forward"; request: Request; responseHeaders: Headers };

const GATE_TTL_MS = 5_000;

interface CachedRead {
  read: PreviewGateRead;
  at: number;
}

const gateCache = new WeakMap<ReleasesBinding, Map<string, CachedRead>>();

export async function readPreviewGate(
  binding: ReleasesBinding,
  slug: string,
  now: () => number = Date.now,
): Promise<PreviewGateRead> {
  let reads = gateCache.get(binding);
  if (!reads) gateCache.set(binding, (reads = new Map()));
  const cached = reads.get(slug);
  if (cached && now() - cached.at < GATE_TTL_MS) return cached.read;
  const read = await fetchPreviewGate(binding, slug);
  if (read.kind === "unreadable") reads.delete(slug);
  else reads.set(slug, { read, at: now() });
  return read;
}

async function fetchPreviewGate(binding: ReleasesBinding, slug: string): Promise<PreviewGateRead> {
  let stored: string | undefined;
  try {
    stored = await binding.readPreviewGate({ slug });
  } catch {
    return { kind: "unreadable" };
  }
  if (stored === undefined) return { kind: "open" };
  const gate = parsePreviewGate(parseJson(stored));
  return gate ? { kind: "gated", gate } : { kind: "unreadable" };
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

function parseJson(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

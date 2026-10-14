import {
  checkGate,
  GATE_CACHE_CONTROL,
  type Gate,
  parseGate,
  ROBOTS_TAG,
} from "@platform/edge-contract/gate";
import type { ReleasesBinding } from "./releases";

export type GateRead = { kind: "open" } | { kind: "gated"; gate: Gate } | { kind: "unreadable" };

export type GateOutcome =
  | { kind: "respond"; response: Response }
  | { kind: "forward"; request: Request; responseHeaders: Headers };

const GATE_TTL_MS = 5_000;

interface CachedRead {
  read: GateRead;
  at: number;
}

const gateCache = new WeakMap<ReleasesBinding, Map<string, CachedRead>>();

export async function readGate(
  binding: ReleasesBinding,
  slug: string,
  now: () => number = Date.now,
): Promise<GateRead> {
  let reads = gateCache.get(binding);
  if (!reads) gateCache.set(binding, (reads = new Map()));
  const cached = reads.get(slug);
  if (cached && now() - cached.at < GATE_TTL_MS) return cached.read;
  const read = await fetchGate(binding, slug);
  if (read.kind === "unanswered") {
    if (cached) {
      console.error(
        `ocel: the releases store did not answer for the gate of ${slug}; keeping the last read`,
        read.error,
      );
      return cached.read;
    }
    console.error(
      `ocel: the releases store did not answer for the gate of ${slug}; answering 503`,
      read.error,
    );
    return { kind: "unreadable" };
  }
  if (read.kind === "unreadable") reads.delete(slug);
  else reads.set(slug, { read, at: now() });
  return read;
}

async function fetchGate(
  binding: ReleasesBinding,
  slug: string,
): Promise<GateRead | { kind: "unanswered"; error: unknown }> {
  let stored: string | undefined;
  try {
    stored = await binding.readGate({ slug });
  } catch (error) {
    return { kind: "unanswered", error };
  }
  if (stored === undefined) return { kind: "open" };
  const gate = parseGate(parseJson(stored));
  return gate ? { kind: "gated", gate } : { kind: "unreadable" };
}

export async function gateRequest(
  request: Request,
  gate: Gate,
  nowSeconds: number,
): Promise<GateOutcome> {
  const url = new URL(request.url);
  const target = url.pathname + url.search;
  const verdict = await checkGate(
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
  return withResponseHeaders(response, new Headers({ "x-robots-tag": ROBOTS_TAG }));
}

export function gateUnreadableResponse(): Response {
  return new Response("Service temporarily unavailable.", {
    status: 503,
    headers: {
      "content-type": "text/plain; charset=utf-8",
      "cache-control": GATE_CACHE_CONTROL,
      "x-robots-tag": ROBOTS_TAG,
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

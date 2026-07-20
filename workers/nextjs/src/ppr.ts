// Partial Prerendering at the edge: a PPR route's cached shell is flushed to the
// client immediately while the origin resumes rendering only the deferred
// Suspense boundaries, and the two halves stream to the client as one response.
//
// The composed response is per-visitor by construction — the resumed half is
// rendered against this request's cookies and headers — so it must never enter a
// shared cache. That is why this path bypasses the colo cache outright rather
// than relying on a cache-control header to keep it out.
//
// The commit point is the first shell byte: once it is written, an origin
// failure can only truncate the document, never redirect or re-render it. That
// is inherent to streaming, and it is the price of a shell at edge latency.
import { CACHE_STATUS } from "./cache";
import type { Interception } from "./interception";

export type PprHit = Extract<Interception, { kind: "ppr" }>;

// Next's resume protocol, as its platform guide specifies it: a POST carrying
// the postponed state as the raw body. The build declares the headers in
// pprChain; the only value Next emits today is this one.
const DEFAULT_RESUME_HEADERS = { "next-resume": "1" };

// Per-hop headers describe the connection this worker terminated, not the
// request being forwarded, so they are the only ones dropped.
const HOP_BY_HOP = [
  "connection",
  "keep-alive",
  "proxy-authenticate",
  "proxy-authorization",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
];

// resumeRequest builds the origin call that renders this request's dynamic half.
//
// It deliberately carries the client's FULL headers — cookie, authorization,
// RSC, user-agent — rather than the allowHeader set a cacheable prerender is
// forwarded under. Those headers are the entire reason the deferred boundaries
// exist: filtering them would render the dynamic half for nobody in particular.
// That is safe here precisely because the result is never cached.
export function resumeRequest(
  url: URL,
  request: Request,
  postponed: string,
  chain: Record<string, string> = DEFAULT_RESUME_HEADERS,
): Request {
  const headers = new Headers(request.headers);
  for (const name of HOP_BY_HOP) headers.delete(name);

  const body = new TextEncoder().encode(postponed);
  headers.set("content-type", "text/plain;charset=UTF-8");
  headers.set("content-length", String(body.byteLength));
  for (const [name, value] of Object.entries(chain)) headers.set(name, value);

  return new Request(url, {
    method: "POST",
    headers,
    body,
    redirect: "manual",
  });
}

// composePpr returns the single response the client sees: the shell's bytes
// followed by the resumed render's. `resumed` must already be in flight — the
// whole point is that the origin is working while the shell is on the wire.
export function composePpr(hit: PprHit, resumed: Promise<Response>): Response {
  const { readable, writable } = new TransformStream<Uint8Array, Uint8Array>();
  void pipe(hit.shell, resumed, writable);

  // The cacheable half (the shell) came from the R2 ISR store, so the composed
  // response is a PRERENDER serve — whether or not the shell was stale, which
  // only decides the background refresh, not the header.
  const headers = new Headers(hit.shell.headers);
  headers.set("cache-control", "private, no-store");
  headers.set(CACHE_STATUS, "PRERENDER");
  headers.delete("content-length");

  return new Response(readable, { status: hit.shell.status, headers });
}

async function pipe(
  shell: Response,
  resumed: Promise<Response>,
  writable: WritableStream<Uint8Array>,
): Promise<void> {
  // A failed resume has an error page for a body, and appending that to the
  // shell would corrupt the document — a truncated page is the better failure.
  // Settle the promise here, before the shell flush awaits, so its eventual
  // rejection is never momentarily unhandled while the shell is on the wire.
  const dynamic = resumed.then(
    async (response) => {
      if (response.ok) return response.body;
      await response.body?.cancel();
      return null;
    },
    () => null,
  );

  const writer = writable.getWriter();
  try {
    await writer.write(new Uint8Array(await shell.arrayBuffer()));
  } catch {
    void dynamic.then((body) => body?.cancel());
    await writable.close().catch(() => {});
    return;
  }

  const body = await dynamic;
  writer.releaseLock();
  if (!body) {
    await writable.close().catch(() => {});
    return;
  }
  await body.pipeTo(writable).catch(() => {});
}

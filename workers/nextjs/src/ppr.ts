import { CACHE_STATUS } from "./cache";
import type { Interception } from "./interception";

export type PprHit = Extract<Interception, { kind: "ppr" }>;

const DEFAULT_RESUME_HEADERS = { "next-resume": "1" };

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

export function composePpr(hit: PprHit, resumed: Promise<Response>): Response {
  const { readable, writable } = new TransformStream<Uint8Array, Uint8Array>();
  void pipe(hit.shell, resumed, writable);

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
  const dynamic = resumed.then(
    async (response) => {
      if (response.ok) return response.body;
      await response.body?.cancel();
      console.error(`ppr resume dropped: origin answered ${response.status}`);
      return null;
    },
    (err) => {
      console.error(`ppr resume dropped: ${errorName(err)}`);
      return null;
    },
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

function errorName(err: unknown): string {
  return err instanceof Error ? err.name : typeof err;
}

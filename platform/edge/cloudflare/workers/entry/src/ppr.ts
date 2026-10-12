export const POSTPONED_LENGTH_HEADER = "x-ocel-postponed-length";

export const PPR_SHELL_VARIANT = ".ppr-shell";

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

export interface PostponedShell {
  shell: Response;
  postponed: string;
}

export async function readPostponedShell(
  response: Response,
): Promise<PostponedShell | "unframed" | "malformed"> {
  const declared = response.headers.get(POSTPONED_LENGTH_HEADER);
  if (declared === null) return "unframed";
  const length = Number(declared);
  const bytes = new Uint8Array(await response.arrayBuffer());
  const headers = new Headers(response.headers);
  headers.delete(POSTPONED_LENGTH_HEADER);
  headers.delete("content-length");
  if (!Number.isInteger(length) || length < 0 || length > bytes.byteLength) return "malformed";
  return {
    shell: new Response(bytes.slice(length), { status: response.status, headers }),
    postponed: new TextDecoder().decode(bytes.slice(0, length)),
  };
}

export function buildResumeRequest(
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

  return new Request(url, { method: "POST", headers, body, redirect: "manual" });
}

function buildPprHeaders(shell: Response): Headers {
  const headers = new Headers(shell.headers);
  headers.set("cache-control", "private, no-store");
  headers.delete("content-length");
  return headers;
}

export function composePpr(shell: Response, resumed: Promise<Response>): Response {
  const { readable, writable } = new TransformStream<Uint8Array, Uint8Array>();
  void pipe(shell, resumed, writable);
  return new Response(readable, { status: shell.status, headers: buildPprHeaders(shell) });
}

export function shapePprHead(shell: Response): Response {
  void shell.body?.cancel();
  return new Response(null, { status: shell.status, headers: buildPprHeaders(shell) });
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
      console.error(`ocel: PPR resume dropped: the origin answered ${response.status}`);
      return null;
    },
    (error) => {
      console.error(
        `ocel: PPR resume dropped: ${error instanceof Error ? error.name : typeof error}`,
      );
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

import { newMetadataToken } from "./metadata-token.mjs";

export interface CloudStorageOptions {
  bucket: string;
  endpoint?: string;
  fetch?: typeof fetch;
  metadataOrigin?: string;
  now?: () => number;
  sleep?: (ms: number) => Promise<void>;
  random?: () => number;
  requestTimeoutMs?: number;
}

export type ObjectRead =
  | { status: "found"; body: string; generation: string }
  | { status: "absent" }
  | { status: "unchanged" };

export type ObjectStream =
  | { status: "found"; body: ReadableStream | null; size: number | null; etag: string }
  | { status: "absent" };

export type ObjectWrite = { status: "written"; generation: string } | { status: "lost" };

export interface CloudStorage {
  read(name: string, conditions?: { ifGenerationNotMatch?: string }): Promise<ObjectRead>;
  open(name: string): Promise<ObjectStream>;
  write(
    name: string,
    body: string,
    conditions?: { ifGenerationMatch?: string },
  ): Promise<ObjectWrite>;
}

const attempts = 4;

export function newCloudStorage(options: CloudStorageOptions): CloudStorage {
  const { bucket } = options;
  const origin = (options.endpoint ?? "https://storage.googleapis.com").replace(/\/$/, "");
  const doFetch = options.fetch ?? globalThis.fetch;
  const sleep =
    options.sleep ?? ((ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms)));
  const random = options.random ?? Math.random;
  const requestTimeoutMs = options.requestTimeoutMs ?? 10_000;

  const metadataToken = newMetadataToken({
    service: "Cloud Storage",
    fetch: doFetch,
    metadataOrigin: options.metadataOrigin,
    now: options.now,
  });

  async function send(url: string, init: RequestInit, signal: AbortSignal): Promise<Response> {
    const headers = new Headers(init.headers);
    if (options.endpoint === undefined)
      headers.set("Authorization", `Bearer ${await metadataToken.token()}`);
    return doFetch(url, { ...init, headers, signal });
  }

  const isTransportError = (error: unknown) =>
    error instanceof Error && (error.name === "TypeError" || error.name === "AbortError");

  async function request(
    verb: "read" | "write",
    name: string,
    url: string,
    init: RequestInit,
    accept: (res: Response) => Promise<unknown> | undefined,
  ): Promise<{ value: unknown; ambiguous: boolean }> {
    const fail = (cause: string) =>
      new Error(`ocel: Cloud Storage ${verb} of ${bucket}/${name} failed: ${cause}`);
    let refreshed = false;
    let ambiguous = false;
    let lastCause = "";
    for (let attempt = 1; attempt <= attempts; attempt++) {
      if (attempt > 1) {
        await sleep(random() * Math.min(2000, 100 * 2 ** (attempt - 2)));
      }
      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), requestTimeoutMs);
      try {
        let res: Response;
        try {
          res = await send(url, init, controller.signal);
          if (res.status === 401 && !refreshed && options.endpoint === undefined) {
            refreshed = true;
            metadataToken.forget();
            res = await send(url, init, controller.signal);
          }
        } catch (error) {
          lastCause = error instanceof Error ? error.message : String(error);
          ambiguous = true;
          continue;
        }
        let accepted: unknown;
        try {
          accepted = await accept(res);
        } catch (error) {
          if (!isTransportError(error)) throw error;
          lastCause = (error as Error).message;
          ambiguous = true;
          continue;
        }
        if (accepted) return { value: accepted, ambiguous };
        lastCause = String(res.status);
        const retryable = res.status === 408 || res.status === 429 || res.status >= 500;
        if (!retryable) throw fail(lastCause);
        ambiguous ||= res.status !== 429;
      } finally {
        clearTimeout(timer);
      }
    }
    throw fail(lastCause);
  }

  async function read(name: string, conditions?: { ifGenerationNotMatch?: string }) {
    const query = new URLSearchParams({ alt: "media" });
    if (conditions?.ifGenerationNotMatch !== undefined) {
      query.set("ifGenerationNotMatch", conditions.ifGenerationNotMatch);
    }
    const url = `${origin}/storage/v1/b/${bucket}/o/${encodeURIComponent(name)}?${query}`;
    const { value } = await request("read", name, url, { method: "GET" }, (res) => {
      if (res.status === 404) return Promise.resolve<ObjectRead>({ status: "absent" });
      if (res.status === 304) return Promise.resolve<ObjectRead>({ status: "unchanged" });
      if (res.status !== 200) return undefined;
      return (async (): Promise<ObjectRead> => {
        const generation = res.headers.get("x-goog-generation");
        if (generation === null) {
          throw new Error(`ocel: Cloud Storage answered ${name} with no generation`);
        }
        return { status: "found", body: await res.text(), generation };
      })();
    });
    return value as ObjectRead;
  }

  async function open(name: string): Promise<ObjectStream> {
    const url = `${origin}/storage/v1/b/${bucket}/o/${encodeURIComponent(name)}?alt=media`;
    const { value } = await request("read", name, url, { method: "GET" }, (res) => {
      if (res.status === 404) return Promise.resolve<ObjectStream>({ status: "absent" });
      if (res.status !== 200) return undefined;
      const generation = res.headers.get("x-goog-generation");
      if (generation === null) {
        throw new Error(`ocel: Cloud Storage answered ${name} with no generation`);
      }
      const length = res.headers.get("content-length");
      return Promise.resolve<ObjectStream>({
        status: "found",
        body: res.body,
        size: length === null ? null : Number(length),
        etag: `"${generation}"`,
      });
    });
    return value as ObjectStream;
  }

  return {
    read,
    open,

    async write(name, body, conditions) {
      const query = new URLSearchParams({ uploadType: "media", name });
      if (conditions?.ifGenerationMatch !== undefined) {
        query.set("ifGenerationMatch", conditions.ifGenerationMatch);
      }
      const url = `${origin}/upload/storage/v1/b/${bucket}/o?${query}`;
      const { value, ambiguous } = await request(
        "write",
        name,
        url,
        { method: "POST", body, headers: { "Content-Type": "application/json" } },
        (res) => {
          if (res.status === 412) return Promise.resolve<ObjectWrite>({ status: "lost" });
          if (res.status !== 200) return undefined;
          return (res.json() as Promise<{ generation: string | number }>).then(
            (json): ObjectWrite => ({
              status: "written",
              generation: String(json.generation),
            }),
          );
        },
      );
      const written = value as ObjectWrite;
      if (written.status === "lost" && ambiguous) {
        const current = await read(name);
        if (current.status === "found" && current.body === body) {
          return { status: "written", generation: current.generation };
        }
      }
      return written;
    },
  };
}

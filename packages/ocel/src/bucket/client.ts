import type { Bucket } from "./bucket.js";
import type { AnyUploader, Uploader, UploadStatusState } from "./types.js";

type Uploaders<B> = B extends Bucket<infer U> ? U : never;
type UploaderName<B> = keyof Uploaders<B> & string;
type InputOf<B, K extends UploaderName<B>> =
  Uploaders<B>[K] extends Uploader<infer I, any, any> ? I : never;

type UploadArgs<B, K extends UploaderName<B>> = {
  files: BrowserFile[];
} & (InputOf<B, K> extends undefined ? { input?: undefined } : { input: InputOf<B, K> });

export interface BrowserFile {
  name: string;
  size: number;
  type: string;
}

interface FetchResponse {
  ok: boolean;
  status: number;
  json(): Promise<any>;
}

type FetchLike = (
  url: string,
  init?: {
    method?: string;
    body?: unknown;
    headers?: Record<string, string>;
  },
) => Promise<FetchResponse>;

export interface UploadClientOptions {
  url: string;
  pollIntervalMs?: number;
  maxPollMs?: number;
  fetch?: FetchLike;
}

export interface UploadResultFile {
  key: string;
  name: string;
}

export interface UploadCallbacks {
  onClientUploadComplete?: (result: { files: UploadResultFile[] }) => void;
  onError?: (error: Error) => void;
}

interface PresignResponse {
  sessionId: string;
  files: {
    url: string;
    key: string;
    name: string;
    contentDisposition?: string;
    headers?: Record<string, string>;
    method?: string;
    fields?: Record<string, string>;
  }[];
}

interface PollResponse {
  state: UploadStatusState;
  error?: string;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

const pollCeilingMs = 15_000;

async function pollUntilTerminal(
  fetchImpl: FetchLike,
  baseUrl: string,
  sessionId: string,
  intervalMs: number,
  maxMs: number,
): Promise<PollResponse> {
  const deadline = Date.now() + maxMs;
  let wait = intervalMs;
  for (;;) {
    const res = await fetchImpl(`${baseUrl}?op=poll&sessionId=${encodeURIComponent(sessionId)}`);
    const body = (await res.json()) as PollResponse;
    if (body.state === "succeeded" || body.state === "expired") return body;
    if (Date.now() >= deadline) return { state: "expired" };
    await sleep(wait);
    wait = Math.min(wait * 2, pollCeilingMs);
  }
}

export function createUploadClient<B extends Bucket<Record<string, AnyUploader>>>(
  options: UploadClientOptions,
) {
  const fetchImpl = options.fetch ?? (globalThis.fetch as unknown as FetchLike);
  const intervalMs = options.pollIntervalMs ?? 1000;
  const maxMs = options.maxPollMs ?? 15 * 60 * 1000;

  async function upload<K extends UploaderName<B>>(
    name: K,
    args: UploadArgs<B, K>,
    callbacks: UploadCallbacks = {},
  ): Promise<{ files: UploadResultFile[] }> {
    try {
      const presignRes = await fetchImpl(`${options.url}?op=presign`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          uploader: name,
          input: (args as { input?: unknown }).input,
          files: args.files.map((f) => ({
            name: f.name,
            size: f.size,
            mimeType: f.type,
          })),
        }),
      });
      if (!presignRes.ok) {
        throw new Error(`presign failed (${presignRes.status})`);
      }
      const presign = (await presignRes.json()) as PresignResponse;

      await Promise.all(
        presign.files.map((target, i) => {
          const method = target.method || "PUT";
          const file = args.files[i];
          if (target.fields && Object.keys(target.fields).length > 0) {
            const form = new FormData();
            for (const [name, value] of Object.entries(target.fields)) form.append(name, value);
            if (target.contentDisposition) {
              form.append("Content-Disposition", target.contentDisposition);
            }
            form.append("file", file as unknown as Blob);
            return fetchImpl(target.url, { method, body: form }).then((res) => {
              if (!res.ok) {
                throw new Error(`upload ${method} failed (${res.status})`);
              }
            });
          }
          const headers = {
            ...target.headers,
            ...(target.contentDisposition
              ? { "content-disposition": target.contentDisposition }
              : {}),
          };
          return fetchImpl(target.url, {
            method,
            body: file,
            headers,
          }).then((res) => {
            if (!res.ok) {
              throw new Error(`upload ${method} failed (${res.status})`);
            }
          });
        }),
      );

      await fetchImpl(`${options.url}?op=complete`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ sessionId: presign.sessionId }),
      });

      const status = await pollUntilTerminal(
        fetchImpl,
        options.url,
        presign.sessionId,
        intervalMs,
        maxMs,
      );

      if (status.state !== "succeeded") {
        throw new Error(
          status.state === "expired" ? "upload expired" : (status.error ?? "upload failed"),
        );
      }

      const result = {
        files: presign.files.map((t) => ({ key: t.key, name: t.name })),
      };
      callbacks.onClientUploadComplete?.(result);
      return result;
    } catch (err) {
      const error = err instanceof Error ? err : new Error(String(err));
      callbacks.onError?.(error);
      throw error;
    }
  }

  return { upload };
}

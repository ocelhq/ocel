import type { Bucket } from "./bucket";
import type { AnyUploader, Uploader, UploadStatusState } from "./types";

type Uploaders<B> = B extends Bucket<infer U> ? U : never;
type UploaderName<B> = keyof Uploaders<B> & string;
type InputOf<B, K extends UploaderName<B>> =
  Uploaders<B>[K] extends Uploader<infer I, any, any> ? I : never;

type UploadArgs<B, K extends UploaderName<B>> = {
  files: BrowserFile[];
} & (InputOf<B, K> extends undefined
  ? { input?: undefined }
  : { input: InputOf<B, K> });

/** The browser File surface the client needs: identity for presign + the body to PUT. */
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
  /** Fetch implementation; defaults to the global fetch. Injectable for tests. */
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
  }[];
}

interface PollResponse {
  state: UploadStatusState;
  error?: string;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function pollUntilTerminal(
  fetchImpl: FetchLike,
  baseUrl: string,
  sessionId: string,
  intervalMs: number,
  maxMs: number,
): Promise<PollResponse> {
  const deadline = Date.now() + maxMs;
  for (;;) {
    const res = await fetchImpl(
      `${baseUrl}?op=poll&sessionId=${encodeURIComponent(sessionId)}`,
    );
    const body = (await res.json()) as PollResponse;
    if (body.state === "succeeded" || body.state === "expired") return body;
    if (Date.now() >= deadline) return { state: "expired" };
    await sleep(intervalMs);
  }
}

/**
 * Builds a typed upload client for a bucket. The bucket is imported as a
 * TYPE only, so no server code reaches the browser bundle. `upload`'s uploader
 * name and input are compile-checked against `typeof bucket`.
 */
export function createUploadClient<B extends Bucket<Record<string, AnyUploader>>>(
  options: UploadClientOptions,
) {
  const fetchImpl = options.fetch ?? (globalThis.fetch as unknown as FetchLike);
  const intervalMs = options.pollIntervalMs ?? 1000;
  const maxMs = options.maxPollMs ?? 2 * 60 * 60 * 1000;

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
          // Content-Type rides implicitly from the File body; Content-Disposition
          // must be sent explicitly so the signed presigned PUT binds it onto the
          // stored object.
          const headers = target.contentDisposition
            ? { "content-disposition": target.contentDisposition }
            : undefined;
          return fetchImpl(target.url, {
            method: "PUT",
            body: args.files[i],
            headers,
          });
        }),
      );

      const status = await pollUntilTerminal(
        fetchImpl,
        options.url,
        presign.sessionId,
        intervalMs,
        maxMs,
      );

      if (status.state !== "succeeded") {
        throw new Error(
          status.state === "expired"
            ? "upload expired"
            : (status.error ?? "upload failed"),
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

import { Code, ConnectError } from "@connectrpc/connect";
import {
  SignedAudience,
  SignedOperation,
  type ObjectInfo as WireObjectInfo,
} from "../gen/proto/app/bucket/v1/bucket_pb.js";
import type { BucketContext } from "./bucket-context.js";
import { ObjectNotFoundError, PreconditionFailedError } from "./errors.js";

/** What a bucket knows about one of its objects. */
export interface ObjectInfo {
  /** The key the object is addressed by. */
  key: string;
  /** The object's length in bytes. */
  size: number;
  /** The store's opaque version tag for these bytes. */
  etag: string;
  /** The media type the object was written with. */
  contentType: string;
  /** When the object last took its current bytes, where the store reports it. */
  uploadedAt: Date | undefined;
  /** The user metadata written alongside the object. */
  metadata: Record<string, string>;
}

/** An object's bytes, with the info that came back beside them. */
export interface ObjectBody {
  /** What the bucket knows about the object. */
  info: ObjectInfo;
  /** The bytes, as a stream that may be read once. */
  body: ReadableStream<Uint8Array>;
  /** The bytes decoded as UTF-8. */
  text(): Promise<string>;
  /** The bytes parsed as JSON. */
  json<T = unknown>(): Promise<T>;
  /** The bytes. */
  bytes(): Promise<Uint8Array>;
}

/** A body a caller may hand to `put`. */
export type PutBody = string | Uint8Array | Blob | ReadableStream<Uint8Array>;

/** How a write is conditioned and described. */
export interface PutOptions {
  /** The media type to store the object under. */
  contentType?: string;
  /** The cache-control the store should serve the object with. */
  cacheControl?: string;
  /** User metadata to keep beside the object, capped at 2 KB. */
  metadata?: Record<string, string>;
  /** `"*"` writes only when no object exists under the key. */
  ifNoneMatch?: "*";
  /** Writes only when the object's current etag matches. */
  ifMatch?: string;
  /** Abandons the write, including every part in flight. */
  signal?: AbortSignal;
}

/** Which bytes of an object to read. */
export interface GetOptions {
  /** The byte range to read; `length` omitted reads to the end. */
  range?: { offset: number; length?: number };
}

/** How a listing is bounded. */
export interface ListOptions {
  /** Only keys under this prefix. */
  prefix?: string;
  /** How many objects one page contains, at most 1000. */
  limit?: number;
}

/** One page of a listing. */
export interface ObjectPage {
  /** The objects on this page. */
  objects: ObjectInfo[];
  /** The cursor that continues the listing, empty when this was the last page. */
  cursor: string;
}

/** A listing, walked as an async iterable or one page at a time. */
export interface ObjectListing extends AsyncIterable<ObjectInfo> {
  /** One page, starting at `cursor` when given. */
  page(options?: { cursor?: string }): Promise<ObjectPage>;
}

/** How a signed read is bounded. */
export interface SignedUrlOptions {
  /** How long the url stays valid, in seconds. */
  expiresIn?: number;
  /** Serves the object as a download, under this filename when a string. */
  download?: boolean | string;
}

/** How a signed write is bounded. */
export interface SignedUploadOptions {
  /** How long the target stays valid, in seconds. */
  expiresIn?: number;
  /** The largest body the target accepts, in bytes. */
  maxSize?: number;
  /** The media type the target accepts. */
  contentType?: string;
}

/** A target an uploader drives itself. */
export interface SignedUpload {
  /** Where to send the body. */
  url: string;
  /** The HTTP method to send it with. */
  method: string;
  /** Headers the signature covers, which the caller must send unchanged. */
  headers: Record<string, string>;
  /** Form fields a POST upload sends with the body; empty for a PUT target. */
  fields: Record<string, string>;
}

/** The object operations a bucket handle exposes. */
export interface BucketObjects {
  /** Writes `body` under `key`, in one request or in parts, and returns the stored object. */
  put(key: string, body: PutBody, options?: PutOptions): Promise<ObjectInfo>;
  /** Reads the object under `key`, or `null` when the bucket has none. */
  get(key: string, options?: GetOptions): Promise<ObjectBody | null>;
  /** What the bucket knows about `key`, or `null` when it has none. */
  head(key: string): Promise<ObjectInfo | null>;
  /** Removes one key or many; a key that is not there is not an error. */
  delete(key: string | string[]): Promise<void>;
  /** The objects under a prefix. */
  list(options?: ListOptions): ObjectListing;
  /** Copies `source` to `destination` within the bucket. */
  copy(source: string, destination: string): Promise<ObjectInfo>;
  /** A url that reads `key` without a credential, for as long as it is valid. */
  signedUrl(key: string, options?: SignedUrlOptions): Promise<string>;
  /** A target that writes `key` without a credential, for as long as it is valid. */
  signedUpload(key: string, options?: SignedUploadOptions): Promise<SignedUpload>;
  /** The object's address on a bucket declared `public: true`. */
  publicUrl(key: string): string;
}

const singleRequestCeiling = 16 * 1024 * 1024;
const partSize = 8 * 1024 * 1024;
const partsInFlight = 4;

type FetchLike = typeof globalThis.fetch;

function info(wire: WireObjectInfo | undefined, key: string): ObjectInfo {
  return {
    key: wire?.key || key,
    size: Number(wire?.size ?? 0n),
    etag: wire?.etag ?? "",
    contentType: wire?.contentType ?? "",
    uploadedAt: wire?.uploadedAt ? timestampDate(wire.uploadedAt) : undefined,
    metadata: wire?.metadata ?? {},
  };
}

function timestampDate(ts: { seconds: bigint; nanos: number }): Date {
  return new Date(Number(ts.seconds) * 1000 + Math.floor(ts.nanos / 1_000_000));
}

function refused(key: string, status: number): Error | undefined {
  if (status === 404) return new ObjectNotFoundError(key);
  if (status === 412 || status === 409) return new PreconditionFailedError(key);
  return undefined;
}

function wireRefusal(key: string, error: unknown): Error {
  if (error instanceof ConnectError) {
    if (error.code === Code.NotFound) return new ObjectNotFoundError(key);
    if (error.code === Code.FailedPrecondition) return new PreconditionFailedError(key);
  }
  if (typeof error === "object" && error !== null && "code" in error) {
    const code = (error as { code: unknown }).code;
    if (code === Code.NotFound) return new ObjectNotFoundError(key);
    if (code === Code.FailedPrecondition) return new PreconditionFailedError(key);
  }
  return error instanceof Error ? error : new Error(String(error));
}

async function sized(
  body: PutBody,
): Promise<{ bytes?: Uint8Array; stream?: ReadableStream<Uint8Array> }> {
  if (typeof body === "string") return { bytes: new TextEncoder().encode(body) };
  if (body instanceof Uint8Array) return { bytes: body };
  if (typeof Blob !== "undefined" && body instanceof Blob) {
    return body.size <= singleRequestCeiling
      ? { bytes: new Uint8Array(await body.arrayBuffer()) }
      : { stream: body.stream() as ReadableStream<Uint8Array> };
  }
  return { stream: body as ReadableStream<Uint8Array> };
}

async function* chunked(
  stream: ReadableStream<Uint8Array>,
  size: number,
): AsyncGenerator<Uint8Array> {
  const reader = stream.getReader();
  let buffered: Uint8Array[] = [];
  let length = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (value) {
        buffered.push(value);
        length += value.byteLength;
      }
      while (length >= size) {
        const joined = concat(buffered, length);
        yield joined.subarray(0, size);
        const rest = joined.subarray(size);
        buffered = rest.byteLength > 0 ? [rest] : [];
        length = rest.byteLength;
      }
      if (done) break;
    }
    if (length > 0) yield concat(buffered, length);
  } finally {
    await reader.cancel().catch(() => {});
  }
}

function concat(chunks: Uint8Array[], length: number): Uint8Array {
  if (chunks.length === 1) return chunks[0] as Uint8Array;
  const out = new Uint8Array(length);
  let at = 0;
  for (const chunk of chunks) {
    out.set(chunk, at);
    at += chunk.byteLength;
  }
  return out;
}

/** Builds the object operations against a resolved runtime context. */
export function createObjects(deps: {
  runtime: () => BucketContext;
  fetch?: FetchLike;
}): BucketObjects {
  const send: FetchLike = deps.fetch ?? ((...args) => globalThis.fetch(...args));

  const sign = async (
    key: string,
    operation: SignedOperation,
    audience: SignedAudience,
    constraints?: {
      contentType?: string;
      maxSize?: number;
      downloadFilename?: string;
      ifNoneMatch?: string;
      ifMatch?: string;
      cacheControl?: string;
      metadata?: Record<string, string>;
      expiresIn?: number;
    },
  ) => {
    const { client, bucket } = deps.runtime();
    const res = await client.sign({
      bucket,
      key,
      operation,
      audience,
      expiresIn: constraints?.expiresIn
        ? { seconds: BigInt(constraints.expiresIn), nanos: 0 }
        : undefined,
      constraints: {
        contentType: constraints?.contentType ?? "",
        maxSize: BigInt(constraints?.maxSize ?? 0),
        downloadFilename: constraints?.downloadFilename ?? "",
        ifNoneMatch: constraints?.ifNoneMatch ?? "",
        ifMatch: constraints?.ifMatch ?? "",
        cacheControl: constraints?.cacheControl ?? "",
        metadata: constraints?.metadata ?? {},
      },
    });
    const target = res.target;
    if (!target) throw new Error(`the runtime signed nothing for "${key}"`);
    return target;
  };

  const putSingle = async (key: string, bytes: Uint8Array, options: PutOptions) => {
    const target = await sign(key, SignedOperation.PUT, SignedAudience.INTERNAL, {
      contentType: options.contentType,
      ifNoneMatch: options.ifNoneMatch,
      ifMatch: options.ifMatch,
      cacheControl: options.cacheControl,
      metadata: options.metadata,
    });
    const headers: Record<string, string> = { ...target.headers };
    if (options.contentType) headers["content-type"] = options.contentType;
    if (options.cacheControl) headers["cache-control"] = options.cacheControl;
    if (options.ifNoneMatch) headers["if-none-match"] = options.ifNoneMatch;
    if (options.ifMatch) headers["if-match"] = options.ifMatch;
    const res = await send(target.url, {
      method: target.method || "PUT",
      body: bytes,
      headers,
      signal: options.signal,
    });
    if (!res.ok) {
      throw refused(key, res.status) ?? new Error(`writing "${key}" was refused (${res.status})`);
    }
  };

  const putMultipart = async (
    key: string,
    parts: AsyncGenerator<Uint8Array>,
    options: PutOptions,
  ) => {
    const { client, bucket } = deps.runtime();
    const { uploadId } = await client.createMultipart({
      bucket,
      key,
      contentType: options.contentType ?? "",
      cacheControl: options.cacheControl ?? "",
      metadata: options.metadata ?? {},
    });

    const completed: { partNumber: number; etag: string }[] = [];
    const inFlight = new AbortController();
    const giveUp = () => inFlight.abort();
    options.signal?.addEventListener("abort", giveUp);
    let next = 1;
    try {
      for (;;) {
        const batch: { partNumber: number; bytes: Uint8Array }[] = [];
        while (batch.length < partsInFlight) {
          const { done, value } = await parts.next();
          if (done) break;
          batch.push({ partNumber: next++, bytes: value });
        }
        if (batch.length === 0) break;

        const signed = await client.signParts({
          bucket,
          key,
          uploadId,
          partNumbers: batch.map((part) => part.partNumber),
          audience: SignedAudience.INTERNAL,
        });
        const byNumber = new Map(signed.parts.map((part) => [part.partNumber, part]));

        await Promise.all(
          batch.map(async (part) => {
            const target = byNumber.get(part.partNumber);
            if (!target) throw new Error(`the runtime signed no url for part ${part.partNumber}`);
            const res = await send(target.url, {
              method: "PUT",
              body: part.bytes,
              headers: { ...target.headers },
              signal: inFlight.signal,
            });
            if (!res.ok) {
              throw new Error(`part ${part.partNumber} of "${key}" was refused (${res.status})`);
            }
            completed.push({
              partNumber: part.partNumber,
              etag: res.headers.get("etag") ?? "",
            });
          }),
        );
      }

      completed.sort((a, b) => a.partNumber - b.partNumber);
      await client.completeMultipart({
        bucket,
        key,
        uploadId,
        parts: completed,
        ifNoneMatch: options.ifNoneMatch ?? "",
        ifMatch: options.ifMatch ?? "",
      });
    } catch (error) {
      inFlight.abort();
      await parts.return(undefined).catch(() => {});
      await client.abortMultipart({ bucket, key, uploadId }).catch(() => {});
      throw wireRefusal(key, error);
    } finally {
      options.signal?.removeEventListener("abort", giveUp);
    }
  };

  const head = async (key: string): Promise<ObjectInfo | null> => {
    const { client, bucket } = deps.runtime();
    const res = await client.head({ bucket, key });
    return res.object ? info(res.object, key) : null;
  };

  return {
    async put(key, body, options = {}) {
      const source = await sized(body);
      if (source.bytes && source.bytes.byteLength <= singleRequestCeiling) {
        await putSingle(key, source.bytes, options);
      } else {
        const stream =
          source.stream ??
          new ReadableStream<Uint8Array>({
            start(controller) {
              controller.enqueue(source.bytes as Uint8Array);
              controller.close();
            },
          });
        await putMultipart(key, chunked(stream, partSize), options);
      }
      const stored = await head(key);
      if (!stored) throw new ObjectNotFoundError(key);
      return stored;
    },

    async get(key, options = {}) {
      const stored = await head(key);
      if (!stored) return null;
      const target = await sign(key, SignedOperation.GET, SignedAudience.INTERNAL);
      const headers: Record<string, string> = {};
      if (options.range) {
        const { offset, length } = options.range;
        headers.range = `bytes=${offset}-${length === undefined ? "" : offset + length - 1}`;
      }
      const res = await send(target.url, { headers });
      if (res.status === 404) return null;
      if (!res.ok) throw new Error(`reading "${key}" was refused (${res.status})`);
      const body = res.body as ReadableStream<Uint8Array>;
      return {
        info: stored,
        body,
        text: () => new Response(body).text(),
        json: <T>() => new Response(body).json() as Promise<T>,
        bytes: async () => new Uint8Array(await new Response(body).arrayBuffer()),
      };
    },

    head,

    async delete(key) {
      const { client, bucket } = deps.runtime();
      const keys = Array.isArray(key) ? key : [key];
      if (keys.length === 0) return;
      await client.delete({ bucket, keys });
    },

    list(options = {}) {
      const page = async ({ cursor }: { cursor?: string } = {}): Promise<ObjectPage> => {
        const { client, bucket } = deps.runtime();
        const res = await client.list({
          bucket,
          prefix: options.prefix ?? "",
          limit: options.limit ?? 0,
          cursor: cursor ?? "",
        });
        return {
          objects: res.objects.map((entry) => info(entry, entry.key)),
          cursor: res.nextCursor,
        };
      };
      return {
        page,
        async *[Symbol.asyncIterator]() {
          let cursor: string | undefined;
          for (;;) {
            const listed = await page({ cursor });
            yield* listed.objects;
            if (!listed.cursor) return;
            cursor = listed.cursor;
          }
        },
      };
    },

    async copy(source, destination) {
      const { client, bucket } = deps.runtime();
      try {
        const res = await client.copy({ bucket, sourceKey: source, destinationKey: destination });
        return info(res.object, destination);
      } catch (error) {
        throw wireRefusal(source, error);
      }
    },

    async signedUrl(key, options = {}) {
      const target = await sign(key, SignedOperation.GET, SignedAudience.EXTERNAL, {
        expiresIn: options.expiresIn,
        downloadFilename:
          typeof options.download === "string"
            ? options.download
            : options.download
              ? key.split("/").pop()
              : undefined,
      });
      return target.url;
    },

    async signedUpload(key, options = {}) {
      const target = await sign(key, SignedOperation.POST_UPLOAD, SignedAudience.EXTERNAL, {
        expiresIn: options.expiresIn,
        maxSize: options.maxSize,
        contentType: options.contentType,
      });
      return {
        url: target.url,
        method: target.method || "POST",
        headers: target.headers,
        fields: target.fields,
      };
    },

    publicUrl(key) {
      const { publicBaseUrl } = deps.runtime();
      if (!publicBaseUrl) {
        throw new Error(
          `this bucket has no public address, so "${key}" has no public url: declare the bucket with \`public: true\` and give the project a domain to serve it from`,
        );
      }
      const path = key.split("/").map(encodeURIComponent).join("/");
      return `${publicBaseUrl.replace(/\/$/, "")}/${path}`;
    },
  };
}

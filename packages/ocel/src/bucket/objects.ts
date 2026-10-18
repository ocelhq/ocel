import {
  SignedAudience,
  SignedOperation,
  type ObjectInfo as WireObjectInfo,
} from "../gen/proto/app/bucket/v1/bucket_pb.js";
import { RuntimeCallError } from "../runtime/errors.js";
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
  /** When the object last took its current bytes, where the store reports it; absent on what `put` returns. */
  uploadedAt: Date | undefined;
  /** The user metadata written alongside the object. */
  metadata: Record<string, string>;
}

/** An object's bytes, with the info of the version they came from. */
export interface ObjectBody {
  /** What the bucket knows about the version `body` holds. */
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
  /** The media type to store the object under, `application/octet-stream` when omitted. */
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
  /** Writes `body` under `key`, in one request or in parts, and returns the info of the version this write stored. */
  put(key: string, body: PutBody, options?: PutOptions): Promise<ObjectInfo>;
  /**
   * Reads the object under `key`, or `undefined` when the bucket has none. Throws
   * `PreconditionFailedError` when the object keeps changing while it is read.
   */
  get(key: string, options?: GetOptions): Promise<ObjectBody | undefined>;
  /** What the bucket knows about `key`, or `undefined` when it has none. */
  head(key: string): Promise<ObjectInfo | undefined>;
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
const readAttempts = 3;
const defaultContentType = "application/octet-stream";

type FetchLike = typeof globalThis.fetch;

function etagless(key: string): Error {
  return new Error(`the store answered the write of "${key}" without its etag`);
}

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
  if (error instanceof RuntimeCallError) {
    if (error.code === "not_found") return new ObjectNotFoundError(key, { cause: error });
    if (error.code === "failed_precondition") {
      return new PreconditionFailedError(key, { cause: error });
    }
  }
  return error instanceof Error ? error : new Error(String(error), { cause: error });
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

async function* prepended(
  head: Uint8Array[],
  rest: AsyncGenerator<Uint8Array>,
): AsyncGenerator<Uint8Array> {
  try {
    yield* head;
    yield* rest;
  } finally {
    await rest.return(undefined);
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
  runtime: (access: string) => BucketContext;
  fetch?: FetchLike;
}): BucketObjects {
  const send: FetchLike = deps.fetch ?? ((...args) => globalThis.fetch(...args));

  const sign = async (
    { client, bucket }: BucketContext,
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

  const putSingle = async (
    ctx: BucketContext,
    key: string,
    bytes: Uint8Array,
    contentType: string,
    options: PutOptions,
  ): Promise<string> => {
    const target = await sign(ctx, key, SignedOperation.PUT, SignedAudience.INTERNAL, {
      contentType,
      ifNoneMatch: options.ifNoneMatch,
      ifMatch: options.ifMatch,
      cacheControl: options.cacheControl,
      metadata: options.metadata,
    });
    const headers: Record<string, string> = { ...target.headers };
    headers["content-type"] = contentType;
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
    const etag = target.etagHeader ? res.headers.get(target.etagHeader) : null;
    if (!etag) throw etagless(key);
    return etag;
  };

  const putMultipart = async (
    { client, bucket }: BucketContext,
    key: string,
    parts: AsyncGenerator<Uint8Array>,
    contentType: string,
    options: PutOptions,
  ): Promise<{ etag: string; size: number }> => {
    const { uploadId } = await client.createMultipart({
      bucket,
      key,
      contentType,
      cacheControl: options.cacheControl ?? "",
      metadata: options.metadata ?? {},
    });

    const completed: { partNumber: number; etag: string }[] = [];
    const inFlight = new AbortController();
    const giveUp = () => inFlight.abort();
    options.signal?.addEventListener("abort", giveUp);
    let next = 1;
    let size = 0;
    let etag: string;
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
            size += part.bytes.byteLength;
            completed.push({
              partNumber: part.partNumber,
              etag: res.headers.get("etag") ?? "",
            });
          }),
        );
      }

      completed.sort((a, b) => a.partNumber - b.partNumber);
      ({ etag } = await client.completeMultipart({
        bucket,
        key,
        uploadId,
        parts: completed,
        ifNoneMatch: options.ifNoneMatch ?? "",
        ifMatch: options.ifMatch ?? "",
      }));
    } catch (error) {
      inFlight.abort();
      await parts.return(undefined).catch(() => {});
      await client.abortMultipart({ bucket, key, uploadId }).catch(() => {});
      throw wireRefusal(key, error);
    } finally {
      options.signal?.removeEventListener("abort", giveUp);
    }
    if (!etag) throw etagless(key);
    return { etag, size };
  };

  const headIn = async (
    { client, bucket }: BucketContext,
    key: string,
  ): Promise<ObjectInfo | undefined> => {
    const res = await client.head({ bucket, key });
    return res.object ? info(res.object, key) : undefined;
  };

  return {
    async put(key, body, options = {}) {
      const ctx = deps.runtime("put");
      const contentType = options.contentType || defaultContentType;
      const written = (etag: string, size: number): ObjectInfo => ({
        key,
        size,
        etag,
        contentType,
        uploadedAt: undefined,
        metadata: options.metadata ?? {},
      });
      const single = async (bytes: Uint8Array) =>
        written(await putSingle(ctx, key, bytes, contentType, options), bytes.byteLength);

      const source = await sized(body);
      if (source.bytes && source.bytes.byteLength <= singleRequestCeiling) {
        return single(source.bytes);
      }
      const stream =
        source.stream ??
        new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(source.bytes as Uint8Array);
            controller.close();
          },
        });
      const parts = chunked(stream, partSize);
      const first = await parts.next();
      const second = first.done ? first : await parts.next();
      if (second.done) return single(first.done ? new Uint8Array(0) : first.value);
      const { etag, size } = await putMultipart(
        ctx,
        key,
        prepended([first.value, second.value], parts),
        contentType,
        options,
      );
      return written(etag, size);
    },

    async get(key, options = {}) {
      const ctx = deps.runtime("get");
      const headers: Record<string, string> = {};
      if (options.range) {
        const { offset, length } = options.range;
        headers.range = `bytes=${offset}-${length === undefined ? "" : offset + length - 1}`;
      }
      let target: Awaited<ReturnType<typeof sign>> | undefined;
      for (let attempt = 0; attempt < readAttempts; attempt++) {
        const stored = await headIn(ctx, key);
        if (!stored) return undefined;
        target ??= await sign(ctx, key, SignedOperation.GET, SignedAudience.INTERNAL);
        const res = await send(target.url, { headers });
        if (res.status === 404) return undefined;
        if (!res.ok) throw new Error(`reading "${key}" was refused (${res.status})`);
        const etag = target.etagHeader ? res.headers.get(target.etagHeader) : null;
        if (!etag || etag !== stored.etag) {
          await res.body?.cancel().catch(() => {});
          continue;
        }
        const body = res.body as ReadableStream<Uint8Array>;
        return {
          info: stored,
          body,
          text: () => new Response(body).text(),
          json: <T>() => new Response(body).json() as Promise<T>,
          bytes: async () => new Uint8Array(await new Response(body).arrayBuffer()),
        };
      }
      throw new PreconditionFailedError(key, {
        message: `the object under "${key}" changed while it was read`,
      });
    },

    async head(key) {
      return headIn(deps.runtime("head"), key);
    },

    async delete(key) {
      const { client, bucket } = deps.runtime("delete");
      const keys = Array.isArray(key) ? key : [key];
      if (keys.length === 0) return;
      await client.delete({ bucket, keys });
    },

    list(options = {}) {
      const page = async ({ cursor }: { cursor?: string } = {}): Promise<ObjectPage> => {
        const { client, bucket } = deps.runtime("list");
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
      const { client, bucket } = deps.runtime("copy");
      try {
        const res = await client.copy({ bucket, sourceKey: source, destinationKey: destination });
        return info(res.object, destination);
      } catch (error) {
        throw wireRefusal(source, error);
      }
    },

    async signedUrl(key, options = {}) {
      const target = await sign(
        deps.runtime("signedUrl"),
        key,
        SignedOperation.GET,
        SignedAudience.EXTERNAL,
        {
          expiresIn: options.expiresIn,
          downloadFilename:
            typeof options.download === "string"
              ? options.download
              : options.download
                ? key.split("/").pop()
                : undefined,
        },
      );
      return target.url;
    },

    async signedUpload(key, options = {}) {
      const target = await sign(
        deps.runtime("signedUpload"),
        key,
        SignedOperation.POST_UPLOAD,
        SignedAudience.EXTERNAL,
        {
          expiresIn: options.expiresIn,
          maxSize: options.maxSize,
          contentType: options.contentType,
        },
      );
      return {
        url: target.url,
        method: target.method || "POST",
        headers: target.headers,
        fields: target.fields,
      };
    },

    publicUrl(key) {
      const { publicBaseUrl } = deps.runtime("publicUrl");
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

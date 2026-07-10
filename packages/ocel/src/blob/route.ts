import { z } from "zod";
import { UploadState } from "../gen/proto/runtime/v1/runtime_pb";
import type { Bucket } from "./bucket";
import { generateKey } from "./keys";
import { decodeMetadata, encodeMetadata } from "./metadata";
import {
  resolveRuntimeContext,
  type RuntimeContext,
} from "./runtime-context";
import type {
  AnyUploader,
  BlobRequest,
  CompletedFile,
  FileInfo,
  LimitValue,
  UploadStatusState,
} from "./types";

export interface RouteOptions {
  /**
   * The runtime context (typed client + store bucket name). Defaults to a
   * lazily-resolved context built from the injected OCEL_RESOURCE_BUCKET_<id>
   * address. Injected directly in tests and by the dev bridge.
   */
  runtime?: RuntimeContext;
}

const presignBody = z.object({
  uploader: z.string(),
  files: z.array(
    z.object({
      name: z.string(),
      size: z.number().int().nonnegative(),
      mimeType: z.string(),
    }),
  ),
  input: z.unknown().optional(),
});

const callbackBody = z.object({
  sessionId: z.string(),
  signature: z.string(),
  file: z.object({
    key: z.string(),
    name: z.string(),
    size: z.number().int().nonnegative(),
    mimeType: z.string(),
  }),
});

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  }) as Response;
}

function resolveLimit<T>(
  value: LimitValue<unknown, T> | undefined,
  metadata: unknown,
): T | undefined {
  if (typeof value === "function") {
    return (value as (ctx: { metadata: unknown }) => T)({ metadata });
  }
  return value;
}

function mimeMatches(patterns: string[], mimeType: string): boolean {
  return patterns.some((p) => {
    if (p === "*/*" || p === "*") return true;
    if (p.endsWith("/*")) return mimeType.startsWith(`${p.slice(0, -1)}`);
    return p === mimeType;
  });
}

function validateFiles(
  up: AnyUploader,
  files: FileInfo[],
  metadata: unknown,
): string | undefined {
  const maxCount = resolveLimit(up.upload.limits?.maxFileCount, metadata);
  const minCount = resolveLimit(up.upload.limits?.minFileCount, metadata);
  const maxSize = resolveLimit(up.upload.limits?.maxFileSize, metadata);

  if (files.length === 0) return "no files provided";
  if (maxCount !== undefined && files.length > maxCount) {
    return `too many files (max ${maxCount})`;
  }
  if (minCount !== undefined && files.length < minCount) {
    return `too few files (min ${minCount})`;
  }

  for (const file of files) {
    if (up.upload.accept && !mimeMatches(up.upload.accept, file.mimeType)) {
      return `file type '${file.mimeType}' is not accepted`;
    }
    if (maxSize !== undefined && file.size > maxSize) {
      return `file '${file.name}' exceeds max size ${maxSize}`;
    }
  }
  return undefined;
}

/**
 * The route's own URL without its query string. The detector later appends
 * `?op=callback` to reach this same route.
 */
function deriveCallbackBaseUrl(req: BlobRequest): string {
  const u = new URL(req.url);
  return `${u.origin}${u.pathname}`;
}

function opOf(req: BlobRequest): string | null {
  return new URL(req.url).searchParams.get("op");
}

function stateToString(state: UploadState): UploadStatusState {
  switch (state) {
    case UploadState.SUCCEEDED:
      return "succeeded";
    case UploadState.EXPIRED:
      return "expired";
    default:
      return "pending";
  }
}

async function handlePresign(
  bucket: Bucket,
  ctx: RuntimeContext,
  req: BlobRequest,
) {
  const parsed = presignBody.safeParse(await req.json());
  if (!parsed.success) return json({ error: "invalid presign request" }, 400);

  const up = bucket.uploaders[parsed.data.uploader];
  if (!up) {
    return json({ error: `unknown uploader '${parsed.data.uploader}'` }, 404);
  }

  let input: unknown;
  if (up.auth.input) {
    const validated = up.auth.input.safeParse(parsed.data.input);
    if (!validated.success) return json({ error: "invalid input" }, 400);
    input = validated.data;
  }

  let metadata: unknown;
  try {
    metadata = await up.auth.middleware({ req, input });
  } catch (err) {
    return json({ error: errorMessage(err, "unauthorized") }, 401);
  }

  const files = parsed.data.files;
  const invalid = validateFiles(up, files, metadata);
  if (invalid) return json({ error: invalid }, 400);

  const presignFiles = files.map((file) => ({
    key: generateKey(up.upload.path, { file, metadata }),
    name: file.name,
    size: BigInt(file.size),
    mimeType: file.mimeType,
  }));

  const res = await ctx.client.presignUpload({
    bucket: ctx.bucket,
    files: presignFiles,
    metadata: encodeMetadata({ uploader: parsed.data.uploader, metadata }),
    contentDisposition: up.upload.contentDisposition ?? "",
    callbackBaseUrl: deriveCallbackBaseUrl(req),
  });

  return json({
    sessionId: res.sessionId,
    files: res.files.map((t) => ({
      url: t.url,
      key: t.key,
      name: t.name,
      contentDisposition: t.contentDisposition || undefined,
    })),
  });
}

async function handleCallback(
  bucket: Bucket,
  ctx: RuntimeContext,
  req: BlobRequest,
) {
  const parsed = callbackBody.safeParse(await req.json());
  if (!parsed.success) return json({ error: "invalid callback request" }, 400);

  const { sessionId, signature, file } = parsed.data;
  const verify = await ctx.client.verifyUploadSignature({
    sessionId,
    signature,
    file: {
      key: file.key,
      name: file.name,
      size: BigInt(file.size),
      mimeType: file.mimeType,
    },
  });

  if (!verify.valid) return json({ error: "invalid signature" }, 401);

  const envelope = decodeMetadata(verify.metadata);
  const up = bucket.uploaders[envelope.uploader];
  const completed: CompletedFile = {
    key: file.key,
    name: file.name,
    size: file.size,
    mimeType: file.mimeType,
    path: file.key,
  };
  await up?.upload.onUploadComplete?.({
    metadata: envelope.metadata,
    file: completed,
  });

  return json({ ok: true });
}

async function handlePoll(
  ctx: RuntimeContext,
  req: BlobRequest,
) {
  const sessionId = new URL(req.url).searchParams.get("sessionId");
  if (!sessionId) return json({ error: "missing sessionId" }, 400);

  const res = await ctx.client.getUploadStatus({ sessionId });
  return json({
    state: stateToString(res.state),
    error: res.error || undefined,
  });
}

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback;
}

/**
 * Builds the public upload route for a bucket. Returns `{ GET, POST }` Web
 * Fetch handlers that multiplex `?op=presign|callback|poll`. Bytes never flow
 * through this route — only presign requests, signed completion callbacks, and
 * status polls.
 */
export interface RouteHandlers {
  GET: (req: BlobRequest) => Promise<Response>;
  POST: (req: BlobRequest) => Promise<Response>;
}

export function createRouteHandler(
  bucket: Bucket,
  options: RouteOptions = {},
): RouteHandlers {
  let ctx = options.runtime;
  const getCtx = () => (ctx ??= resolveRuntimeContext(bucket));

  async function POST(req: BlobRequest) {
    const op = opOf(req);
    if (op === "presign") return handlePresign(bucket, getCtx(), req);
    if (op === "callback") return handleCallback(bucket, getCtx(), req);
    return json({ error: `unknown op '${op}'` }, 400);
  }

  async function GET(req: BlobRequest) {
    const op = opOf(req);
    if (op === "poll") return handlePoll(getCtx(), req);
    return json({ error: `unknown op '${op}'` }, 400);
  }

  return { GET, POST };
}

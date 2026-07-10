import {
  GetObjectTaggingCommand,
  PutObjectCommand,
  S3Client,
} from "@aws-sdk/client-s3";
import { getSignedUrl } from "@aws-sdk/s3-request-presigner";

// The presigned PUT is valid for 1h; the session TTL is strictly greater (see
// route.ts) so the expiry sweep never races a still-live URL.
const PRESIGN_TTL_S = 60 * 60;

// The tag key carried by the signed x-amz-tagging binding. The detector reads
// this tag off the landed object to resolve which session it belongs to.
const SESSION_TAG_KEY = "sessionId";

// Dev/MinIO-oriented defaults so presign generation (pure signing) works with
// zero config; overridable by env for a real MinIO/S3 target. Presigning is
// pure signing and needs no reachable store.
function blobConfig() {
  return {
    endpoint: process.env.OCEL_BLOB_ENDPOINT ?? "http://localhost:9000",
    region: process.env.OCEL_BLOB_REGION ?? "us-east-1",
    bucket: process.env.OCEL_BLOB_BUCKET ?? "ocel-dev",
    accessKeyId: process.env.OCEL_BLOB_ACCESS_KEY_ID ?? "minioadmin",
    secretAccessKey: process.env.OCEL_BLOB_SECRET_ACCESS_KEY ?? "minioadmin",
  };
}

export function storeBucket(): string {
  return blobConfig().bucket;
}

// Built per call, not cached: it's pure credential assembly (no I/O), so a dev
// env change to OCEL_BLOB_* takes effect immediately, and a module loaded
// before env is populated can't pin the placeholder defaults.
function s3Client(): S3Client {
  const config = blobConfig();
  return new S3Client({
    region: config.region,
    endpoint: config.endpoint,
    forcePathStyle: true,
    credentials: {
      accessKeyId: config.accessKeyId,
      secretAccessKey: config.secretAccessKey,
    },
  });
}

export interface PresignPutArgs {
  key: string;
  contentType: string;
  contentLength: number;
  sessionId: string;
  contentDisposition?: string;
}

// Mints a presigned PUT URL that binds Content-Length (exact) and Content-Type
// as signed conditions and a signed x-amz-tagging = sessionId, so a client that
// lies about size/type or alters the tag fails the store's signature check -
// limits are store-enforced, not just SDK-checked. When contentDisposition is
// set it is signed too, so the client must send it and the store persists it as
// object metadata tamper-resistantly.
export async function presignPut(args: PresignPutArgs): Promise<string> {
  const config = blobConfig();
  const command = new PutObjectCommand({
    Bucket: config.bucket,
    Key: args.key,
    ContentType: args.contentType,
    ContentLength: args.contentLength,
    ContentDisposition: args.contentDisposition || undefined,
    Tagging: `${SESSION_TAG_KEY}=${args.sessionId}`,
  });

  const signableHeaders = new Set(["content-length", "content-type"]);
  if (args.contentDisposition) signableHeaders.add("content-disposition");

  return getSignedUrl(s3Client(), command, {
    expiresIn: PRESIGN_TTL_S,
    // content-length and content-type (and content-disposition, when set) are
    // signed as sent headers - a browser PUT sends them - so the store enforces
    // the exact size/type and binds the disposition onto the object. The session
    // tag hoists into the SigV4-signed query string instead of a signed header:
    // it stays cryptographically bound while a real browser-style PUT - which
    // won't send an arbitrary x-amz-tagging header - still succeeds.
    signableHeaders,
  });
}

// Returns the sessionId tag of the object at key, or undefined if no object has
// landed there yet. The detector reads the tag - not just existence - so it
// only completes a session when the landed object actually belongs to it: with
// randomSuffix off, two sessions can share a key, and the tag is what keeps
// completion collision-safe (mirroring the prod listener's GetObjectTagging).
export async function objectSessionTag(
  key: string,
): Promise<string | undefined> {
  try {
    const { TagSet } = await s3Client().send(
      new GetObjectTaggingCommand({ Bucket: blobConfig().bucket, Key: key }),
    );
    return TagSet?.find((t) => t.Key === SESSION_TAG_KEY)?.Value;
  } catch (err) {
    const status = (err as { $metadata?: { httpStatusCode?: number } })
      .$metadata?.httpStatusCode;
    const name = (err as { name?: string }).name;
    if (status === 404 || name === "NotFound" || name === "NoSuchKey") {
      return undefined;
    }
    throw err;
  }
}

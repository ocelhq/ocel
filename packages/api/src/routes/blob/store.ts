import { PutObjectCommand, S3Client } from "@aws-sdk/client-s3";
import { getSignedUrl } from "@aws-sdk/s3-request-presigner";

// The presigned PUT is valid for 1h; the session TTL is strictly greater (see
// route.ts) so the expiry sweep never races a still-live URL.
const PRESIGN_TTL_S = 60 * 60;

// The tag key carried by the signed x-amz-tagging binding. The detector reads
// this tag off the landed object to resolve which session it belongs to.
const SESSION_TAG_KEY = "sessionId";

// Dev/MinIO-oriented defaults so presign generation (pure signing) works with
// zero config; overridable by env for a real MinIO/S3 target. The container
// itself is added in T4 - generating a presigned URL does not require it.
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

let cachedClient: S3Client | undefined;

function s3Client(): S3Client {
  if (!cachedClient) {
    const config = blobConfig();
    cachedClient = new S3Client({
      region: config.region,
      endpoint: config.endpoint,
      forcePathStyle: true,
      credentials: {
        accessKeyId: config.accessKeyId,
        secretAccessKey: config.secretAccessKey,
      },
    });
  }
  return cachedClient;
}

export interface PresignPutArgs {
  key: string;
  contentType: string;
  contentLength: number;
  sessionId: string;
}

// Mints a presigned PUT URL that binds Content-Length (exact) and Content-Type
// as signed conditions and a signed x-amz-tagging = sessionId, so a client that
// lies about size/type or alters the tag fails the store's signature check -
// limits are store-enforced, not just SDK-checked.
export async function presignPut(args: PresignPutArgs): Promise<string> {
  const config = blobConfig();
  const command = new PutObjectCommand({
    Bucket: config.bucket,
    Key: args.key,
    ContentType: args.contentType,
    ContentLength: args.contentLength,
    Tagging: `${SESSION_TAG_KEY}=${args.sessionId}`,
  });

  return getSignedUrl(s3Client(), command, {
    expiresIn: PRESIGN_TTL_S,
    // Force these into the signed set rather than let them hoist to the query,
    // so Content-Length, Content-Type, and the session tag are all bound.
    // content-length also has no request body at signing time, so it must be
    // added explicitly.
    signableHeaders: new Set(["content-length", "content-type"]),
    unhoistableHeaders: new Set(["x-amz-tagging"]),
  });
}

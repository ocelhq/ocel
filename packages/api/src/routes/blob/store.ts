import {
  GetObjectTaggingCommand,
  PutObjectCommand,
  S3Client,
} from "@aws-sdk/client-s3";
import { getSignedUrl } from "@aws-sdk/s3-request-presigner";

const PRESIGN_TTL_S = 60 * 60;

const SESSION_TAG_KEY = "sessionId";

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
    signableHeaders,
  });
}

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

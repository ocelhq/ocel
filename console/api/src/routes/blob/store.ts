import {
  DeleteObjectsCommand,
  GetObjectTaggingCommand,
  ListObjectsV2Command,
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

export function projectObjectPrefix(organizationId: string, projectId: string): string {
  return `${organizationId}/${projectId}/`;
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

export async function objectSessionTag(key: string): Promise<string | undefined> {
  try {
    const { TagSet } = await s3Client().send(
      new GetObjectTaggingCommand({ Bucket: blobConfig().bucket, Key: key }),
    );
    return TagSet?.find((t) => t.Key === SESSION_TAG_KEY)?.Value;
  } catch (err) {
    const status = (err as { $metadata?: { httpStatusCode?: number } }).$metadata?.httpStatusCode;
    const name = (err as { name?: string }).name;
    if (status === 404 || name === "NotFound" || name === "NoSuchKey") {
      return undefined;
    }
    throw err;
  }
}

export async function deleteProjectObjects(
  organizationId: string,
  projectId: string,
): Promise<void> {
  const bucket = blobConfig().bucket;
  const prefix = projectObjectPrefix(organizationId, projectId);
  const client = s3Client();

  let continuationToken: string | undefined;
  do {
    const page = await client.send(
      new ListObjectsV2Command({
        Bucket: bucket,
        Prefix: prefix,
        ContinuationToken: continuationToken,
      }),
    );
    const objects = (page.Contents ?? []).flatMap(({ Key }) => (Key ? [{ Key }] : []));
    if (objects.length > 0) {
      const { Errors } = await client.send(
        new DeleteObjectsCommand({ Bucket: bucket, Delete: { Objects: objects, Quiet: true } }),
      );
      const refused = Errors ?? [];
      if (refused.length > 0) {
        throw new Error(
          `the blob store kept ${refused.length} object(s) under ${prefix}: ${refused[0].Code} ${refused[0].Message}`,
        );
      }
    }
    continuationToken = page.IsTruncated ? page.NextContinuationToken : undefined;
  } while (continuationToken);
}

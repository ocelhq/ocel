import {
  GetObjectCommand,
  PutObjectCommand,
  S3Client,
} from "@aws-sdk/client-s3";
import { bucket, uploader } from "ocel/blob/express";
import { postgres } from "ocel/postgres";
import sharp from "sharp";
import { z } from "zod";

export const pg = postgres("main");

const blobEndpoint = process.env.OCEL_BLOB_ENDPOINT;
const objectStore = new S3Client(
  blobEndpoint
    ? {
        endpoint: blobEndpoint,
        forcePathStyle: true,
        region: process.env.OCEL_BLOB_REGION ?? "us-east-1",
        credentials:
          process.env.OCEL_BLOB_ACCESS_KEY_ID &&
          process.env.OCEL_BLOB_SECRET_ACCESS_KEY
            ? {
                accessKeyId: process.env.OCEL_BLOB_ACCESS_KEY_ID,
                secretAccessKey: process.env.OCEL_BLOB_SECRET_ACCESS_KEY,
              }
            : undefined,
        maxAttempts: 3,
        retryMode: "standard",
      }
    : { maxAttempts: 3, retryMode: "standard" },
);

export const uploads = bucket("uploads", {
  uploaders: {
    document: uploader(
      {
        input: z.object({ ownerId: z.string() }),
        middleware: ({ input }) => ({ ownerId: input.ownerId }),
      },
      {
        accept: ["image/*"],
        limits: { maxFileCount: 1 },
        path: ({ file, metadata }) =>
          `documents/${metadata.ownerId}/${file.name}`,
        contentDisposition: "inline",
        onUploadComplete: async ({ metadata, file }) => {
          const { rows } = await pg.query<{ id: number }>(
            `INSERT INTO documents (key, name, mime_type, size, owner_id)
             VALUES ($1, $2, $3, $4, $5)
             RETURNING id`,
            [file.key, file.name, file.mimeType, file.size, metadata.ownerId],
          );
          const bucketName =
            process.env.OCEL_BLOB_BUCKET ?? uploads.__config().bucket;
          const original = await objectStore.send(
            new GetObjectCommand({ Bucket: bucketName, Key: file.key }),
          );
          const source = await original.Body?.transformToByteArray();
          if (!source) throw new Error(`uploaded object ${file.key} has no body`);
          const thumbnail = await sharp(source)
            .resize(256, 256, { fit: "inside", withoutEnlargement: true })
            .webp()
            .toBuffer();
          const thumbnailKey = `thumbnails/${file.key}.webp`;
          await objectStore.send(
            new PutObjectCommand({
              Bucket: bucketName,
              Key: thumbnailKey,
              Body: thumbnail,
              ContentType: "image/webp",
            }),
          );
          await pg.query(
            "UPDATE documents SET thumbnail_key = $1 WHERE id = $2",
            [thumbnailKey, rows[0]!.id],
          );
        },
      },
    ),
  },
});

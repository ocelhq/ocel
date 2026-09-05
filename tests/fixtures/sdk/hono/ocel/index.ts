import { bucket, uploader } from "ocel/blob/hono";
import { postgres } from "ocel/postgres";
import { z } from "zod";

export const pg = postgres("main");

export const uploads = bucket("uploads", {
  uploaders: {
    document: uploader(
      {
        input: z.object({ ownerId: z.string() }),
        middleware: ({ input }) => ({ ownerId: input.ownerId }),
      },
      {
        accept: ["image/*", "application/pdf"],
        limits: { maxFileCount: 5 },
        path: { prefix: "documents/" },
        onUploadComplete: async ({ metadata, file }) => {
          await pg.query(
            `INSERT INTO documents (key, name, mime_type, size, owner_id)
             VALUES ($1, $2, $3, $4, $5)`,
            [file.key, file.name, file.mimeType, file.size, metadata.ownerId],
          );
        },
      },
    ),
  },
});

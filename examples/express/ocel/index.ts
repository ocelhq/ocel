import { bucket, uploader } from "ocel/blob/express";
import { postgres } from "ocel/postgres";
import { z } from "zod";

// Declaring a resource *is* the provisioning step: `ocel dev` discovers these
// calls, provisions a Postgres database and a bucket for them, and injects the
// connections into the app's environment.
export const pg = postgres("main");

// A multi-file document uploader: images or PDFs, up to five at a time.
// `input { ownerId }` authorizes and threads through as the owner; each landed
// file is recorded in postgres("main") by onUploadComplete.
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

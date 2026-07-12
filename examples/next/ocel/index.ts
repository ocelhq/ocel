import { bucket, uploader } from "@ocel/sdk/blob/next";
import { postgres } from "@ocel/sdk/postgres";
import { z } from "zod";

// Declaring a resource *is* the provisioning step: `ocel dev` discovers these
// calls, provisions a Postgres database and a bucket for them, and injects the
// connections into the app's environment.
export const pg = postgres("main");

// A single-image avatar uploader. `input { userId }` authorizes the upload and
// threads through as the stored document's owner; onUploadComplete records the
// landed object in postgres("main") - the one server-authoritative write.
export const uploads = bucket("uploads", {
  uploaders: {
    avatar: uploader(
      {
        input: z.object({ userId: z.string() }),
        middleware: ({ input }) => ({ userId: input.userId }),
      },
      {
        accept: ["image/*"],
        limits: { maxFileCount: 1 },
        path: { prefix: "avatars/" },
        contentDisposition: "inline",
        onUploadComplete: async ({ metadata, file }) => {
          await pg.query(
            `INSERT INTO documents (key, name, mime_type, size, owner_id)
             VALUES ($1, $2, $3, $4, $5)`,
            [file.key, file.name, file.mimeType, file.size, metadata.userId],
          );
        },
      },
    ),
  },
});

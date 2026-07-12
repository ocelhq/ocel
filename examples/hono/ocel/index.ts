import { bucket, uploader } from "@ocel/sdk/blob/hono";
import { postgres } from "@ocel/sdk/postgres";
import { z } from "zod";

// Declaring a resource *is* the provisioning step: `ocel dev` discovers these
// calls, provisions a Postgres database and a bucket for them, and injects the
// connections into the app's environment.
export const pg = postgres("main");

// A thread-attachment uploader. `input { threadId }` authorizes the upload and
// drives a *path function* that files each object under its thread; the object
// is served as an attachment. onUploadComplete records the landing in
// postgres("main").
export const uploads = bucket("uploads", {
  uploaders: {
    attachment: uploader(
      {
        input: z.object({ threadId: z.string() }),
        middleware: ({ input }) => ({ threadId: input.threadId }),
      },
      {
        path: ({ file, metadata }) =>
          `threads/${metadata.threadId}/${file.name}`,
        contentDisposition: "attachment",
        onUploadComplete: async ({ metadata, file }) => {
          await pg.query(
            `INSERT INTO documents (key, name, mime_type, size, owner_id)
             VALUES ($1, $2, $3, $4, $5)`,
            [file.key, file.name, file.mimeType, file.size, metadata.threadId],
          );
        },
      },
    ),
  },
});

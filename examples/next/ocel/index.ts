import { bucket, uploader } from "ocel/blob/next";
import { postgres } from "ocel/postgres";
import { z } from "zod";

export const pg = postgres("main");

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

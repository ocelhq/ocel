import { bucket, uploader } from "ocel/blob/next";
import { postgres } from "ocel/postgres";
import { z } from "zod";

export const pg = postgres("main");

export async function migrate() {
  await pg.query(`
    CREATE TABLE IF NOT EXISTS todos (
      id    SERIAL PRIMARY KEY,
      title TEXT    NOT NULL,
      done  BOOLEAN NOT NULL DEFAULT false
    )
  `);
  await pg.query(`
    CREATE TABLE IF NOT EXISTS documents (
      id         SERIAL      PRIMARY KEY,
      key        TEXT        NOT NULL,
      name       TEXT        NOT NULL,
      mime_type  TEXT        NOT NULL,
      size       BIGINT      NOT NULL,
      owner_id   TEXT,
      thumbnail_key TEXT,
      created_at TIMESTAMPTZ NOT NULL DEFAULT now()
    )
  `);
  await pg.query(
    "ALTER TABLE documents ADD COLUMN IF NOT EXISTS thumbnail_key TEXT",
  );
}

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

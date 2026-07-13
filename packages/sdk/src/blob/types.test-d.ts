import { expectTypeOf } from "vitest";
import { z } from "zod";
import { bucket } from "./bucket.js";
import { createUploadClient } from "./client.js";
import { uploader } from "./uploader.js";

// A bucket with two uploaders: `avatar` takes input, `doc` takes none.
const avatar = uploader(
  {
    input: z.object({ userId: z.string() }),
    middleware: ({ input }) => {
      // input is the parsed schema type
      expectTypeOf(input).toEqualTypeOf<{ userId: string }>();
      return { userId: input.userId, plan: "free" as const };
    },
  },
  {
    // metadata from middleware threads into path + limits + onUploadComplete
    limits: {
      maxFileSize: ({ metadata }) => {
        expectTypeOf(metadata).toEqualTypeOf<{ userId: string; plan: "free" }>();
        return 10;
      },
    },
    path: ({ metadata, file }) => `${metadata.userId}/${file.name}`,
    onUploadComplete: ({ metadata, file }) => {
      expectTypeOf(metadata).toEqualTypeOf<{ userId: string; plan: "free" }>();
      expectTypeOf(file.path).toEqualTypeOf<string>();
    },
  },
);

const doc = uploader({ middleware: () => ({ kind: "doc" }) });

const storage = bucket("storage", { uploaders: { avatar, doc } });

const client = createUploadClient<typeof storage>({ url: "/api/upload" });

// Never executed — these assertions are compile-time only.
export async function _typeChecks() {
  // uploader name is compile-checked against the bucket
  await client.upload("avatar", { files: [], input: { userId: "u1" } });
  await client.upload("doc", { files: [] });

  // @ts-expect-error unknown uploader name
  await client.upload("nope", { files: [] });

  // @ts-expect-error missing required input for `avatar`
  await client.upload("avatar", { files: [] });

  // @ts-expect-error input shape mismatch
  await client.upload("avatar", { files: [], input: { userId: 123 } });

  // @ts-expect-error `doc` takes no input
  await client.upload("doc", { files: [], input: { userId: "x" } });
}

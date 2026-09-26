import { expectTypeOf } from "vitest";
import { z } from "zod";
import { bucket } from "./bucket.js";
import { createUploadClient } from "./client.js";
import type { ObjectBody, ObjectInfo } from "./objects.js";
import { uploader } from "./uploader.js";

const avatar = uploader(
  {
    input: z.object({ userId: z.string() }),
    middleware: ({ input }) => {
      expectTypeOf(input).toEqualTypeOf<{ userId: string }>();
      return { userId: input.userId, plan: "free" as const };
    },
  },
  {
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

export async function _typeChecks() {
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

const assets = bucket("assets", { public: true });

export async function _objectTypeChecks() {
  expectTypeOf(await storage.head("a.png")).toEqualTypeOf<ObjectInfo | null>();
  expectTypeOf(await storage.get("a.png")).toEqualTypeOf<ObjectBody | null>();
  expectTypeOf(await storage.put("a.png", "x")).toEqualTypeOf<ObjectInfo>();
  await storage.delete(["a.png", "b.png"]);
  for await (const info of storage.list({ prefix: "a/" })) {
    expectTypeOf(info).toEqualTypeOf<ObjectInfo>();
  }
  expectTypeOf(await storage.signedUrl("a.png")).toEqualTypeOf<string>();

  expectTypeOf(assets.publicUrl("a.png")).toEqualTypeOf<string>();

  // @ts-expect-error a bucket that was not declared public has no public address
  storage.publicUrl("a.png");
}

const uploadersAreOptional = bucket("plain");
export type _PlainBucketStillReads = ReturnType<typeof uploadersAreOptional.head>;

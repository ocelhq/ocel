import { HeadObjectCommand, S3Client } from "@aws-sdk/client-s3";
import type { AnyUploader, Bucket } from "ocel/blob";
import { createUploadClient } from "ocel/blob/client";
import { afterAll, beforeAll, describe, expect, inject, it } from "vitest";
import {
  base,
  clearExampleEnv,
  clearLink,
  type DevHandle,
  type ExampleSpec,
  minioReachable,
  prepareExample,
  runLink,
  runMigrate,
  startDev,
  waitForHealth,
} from "./harness";

async function poll<T>(
  fn: () => Promise<T | undefined>,
  { timeoutMs = 15_000, intervalMs = 250 } = {},
): Promise<T | undefined> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const value = await fn();
    if (value !== undefined) return value;
    if (Date.now() >= deadline) return undefined;
    await new Promise((r) => setTimeout(r, intervalMs));
  }
}

export function describeExample(spec: ExampleSpec) {
  describe(`${spec.framework} example (dev)`, () => {
    const token = inject("accessToken");
    const runId = `${Date.now().toString(36)}-${Math.random()
      .toString(36)
      .slice(2, 7)}`;
    let dev: DevHandle | undefined;
    let createdEnv = false;

    beforeAll(async () => {
      createdEnv = await prepareExample(spec);
      await runLink(spec, token, runId);
      await runMigrate(spec, token);
      dev = startDev(spec, token);
      await waitForHealth(spec, dev);
    }, 180_000);

    afterAll(async () => {
      await dev?.stop();
      await clearLink(spec);
      await clearExampleEnv(spec, createdEnv);
    });

    it("reports health", async () => {
      const response = await fetch(`${base(spec)}/api/health`);
      expect(response.status).toBe(200);
      expect(await response.json()).toEqual({ ok: true });
    });

    it("creates, lists, gets, and deletes a todo", async () => {
      const todos = `${base(spec)}/api/todos`;

      const created = await fetch(todos, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ title: "write e2e tests" }),
      });
      expect(created.status).toBe(201);
      const todo = (await created.json()) as {
        id: number;
        title: string;
        done: boolean;
      };
      expect(todo).toEqual({
        id: expect.any(Number),
        title: "write e2e tests",
        done: false,
      });

      const listed = await fetch(todos);
      expect(listed.status).toBe(200);
      const all = (await listed.json()) as Array<{
        id: number;
        title: string;
        done: boolean;
      }>;
      expect(all.find((candidate) => candidate.id === todo.id)).toEqual(todo);

      const got = await fetch(`${todos}/${todo.id}`);
      expect(got.status).toBe(200);
      expect(await got.json()).toEqual(todo);

      const deleted = await fetch(`${todos}/${todo.id}`, {
        method: "DELETE",
      });
      expect(deleted.status).toBe(204);

      const gone = await fetch(`${todos}/${todo.id}`);
      expect(gone.status).toBe(404);
    });

    it("uploads a file and records it in documents via onUploadComplete", async (ctx) => {
      if (!(await minioReachable())) {
        ctx.skip();
        return;
      }

      const client = createUploadClient<Bucket<Record<string, AnyUploader>>>({
        url: `${base(spec)}/api/upload`,
        pollIntervalMs: 250,
        maxPollMs: 20_000,
      });

      const ownerId = `${spec.framework}-${runId}`;
      const image = Buffer.from(
        "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAAUElEQVR42pXLSQ0AIAwF0UpBCtIqDSlIgQTC2uU3meMbqplDUUgnLhTSgWFodFgaGk7tD492hl9bg6jVQdPyYGhhsPU7uPoaEL0HUM8B131oYrCCEFU/lXsAAAAASUVORK5CYII=",
        "base64",
      );
      const file = new File([image], "photo.png", {
        type: "image/png",
      });

      const clientKeys: string[] = [];
      const result = await client.upload(
        "document",
        { files: [file], input: { ownerId } },
        {
          onClientUploadComplete: ({ files }) => {
            clientKeys.push(...files.map((f) => f.key));
          },
        },
      );

      expect(result.files).toHaveLength(1);
      const key = result.files[0]!.key;
      expect(key).toContain(`documents/${ownerId}/photo.png`);
      expect(clientKeys).toEqual([key]);

      const row = await poll(async () => {
        const res = await fetch(`${base(spec)}/api/documents`);
        if (!res.ok) return undefined;
        const docs = (await res.json()) as Array<{
          id: number;
          key: string;
          name: string;
          mime_type: string;
          size: string;
          owner_id: string | null;
          thumbnail_key: string | null;
        }>;
        const document = docs.find((d) => d.key === key);
        if (
          spec.capabilities.includes("thumbnail") &&
          !document?.thumbnail_key
        ) {
          return undefined;
        }
        return document;
      });

      expect(row).toBeDefined();
      expect(row!.name).toBe("photo.png");
      expect(row!.mime_type).toBe("image/png");
      expect(row!.size).toBe(String(image.byteLength));
      expect(row!.owner_id).toBe(ownerId);
      expect(Object.keys(row!).sort()).toEqual([
        "id",
        "key",
        "mime_type",
        "name",
        "owner_id",
        "size",
        "thumbnail_key",
      ]);

      if (spec.capabilities.includes("thumbnail")) {
        expect(row!.thumbnail_key).toBe(`thumbnails/${key}.webp`);
        const endpoint = process.env.OCEL_BLOB_ENDPOINT;
        const s3 = new S3Client({
          endpoint,
          forcePathStyle: Boolean(endpoint),
          region: process.env.OCEL_BLOB_REGION ?? "us-east-1",
          credentials: {
            accessKeyId:
              process.env.OCEL_BLOB_ACCESS_KEY_ID ?? "minioadmin",
            secretAccessKey:
              process.env.OCEL_BLOB_SECRET_ACCESS_KEY ?? "minioadmin",
          },
        });
        const thumbnail = await s3.send(
          new HeadObjectCommand({
            Bucket: process.env.OCEL_BLOB_BUCKET ?? "ocel-dev",
            Key: row!.thumbnail_key!,
          }),
        );
        expect(thumbnail.ContentType).toBe("image/webp");
      } else {
        expect(row!.thumbnail_key).toBeNull();
      }
    }, 60_000);
  });
}

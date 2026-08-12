import type { AnyUploader, Bucket } from "ocel/blob";
import { createUploadClient } from "ocel/blob/client";
import { afterAll, beforeAll, describe, expect, inject, it } from "vitest";
import {
  base,
  clearLink,
  type DevHandle,
  type ExampleSpec,
  minioReachable,
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
  describe(`${spec.framework} example (e2e)`, () => {
    const token = inject("accessToken");
    const runId = `${Date.now().toString(36)}-${Math.random()
      .toString(36)
      .slice(2, 7)}`;
    let dev: DevHandle | undefined;

    beforeAll(async () => {
      await runLink(spec, token, runId);
      await runMigrate(spec, token);
      dev = startDev(spec, token);
      await waitForHealth(spec, dev);
    }, 180_000);

    afterAll(async () => {
      await dev?.stop();
      await clearLink(spec);
    });

    it("creates, lists, gets, and deletes a todo", async () => {
      const todos = `${base(spec)}${spec.todosPath}`;

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
      expect(todo.title).toBe("write e2e tests");
      expect(todo.done).toBe(false);
      expect(typeof todo.id).toBe("number");

      const listed = await fetch(todos);
      expect(listed.status).toBe(200);
      const all = (await listed.json()) as Array<{ id: number }>;
      expect(all.some((t) => t.id === todo.id)).toBe(true);

      const got = await fetch(`${todos}/${todo.id}`);
      expect(got.status).toBe(200);
      const gotBody = (await got.json()) as { id: number; title: string };
      expect(gotBody.id).toBe(todo.id);
      expect(gotBody.title).toBe("write e2e tests");

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

      const blobSpec = spec.blob;
      const client = createUploadClient<Bucket<Record<string, AnyUploader>>>({
        url: `${base(spec)}${blobSpec.uploadPath}`,
        pollIntervalMs: 250,
        maxPollMs: 20_000,
      });

      const file = new File([Buffer.from("example-bytes")], blobSpec.file.name, {
        type: blobSpec.file.type,
      });

      const clientKeys: string[] = [];
      const result = await client.upload(
        blobSpec.uploaderName,
        { files: [file], input: blobSpec.input },
        {
          onClientUploadComplete: ({ files }) => {
            clientKeys.push(...files.map((f) => f.key));
          },
        },
      );

      expect(result.files).toHaveLength(1);
      const key = result.files[0]!.key;
      for (const part of blobSpec.expectedKeyIncludes) expect(key).toContain(part);
      expect(clientKeys).toEqual([key]);

      const row = await poll(async () => {
        const res = await fetch(`${base(spec)}${blobSpec.documentsPath}`);
        if (!res.ok) return undefined;
        const docs = (await res.json()) as Array<{
          key: string;
          name: string;
          mime_type: string;
          owner_id: string | null;
        }>;
        return docs.find((d) => d.key === key);
      });

      expect(row).toBeDefined();
      expect(row!.name).toBe(blobSpec.file.name);
      expect(row!.mime_type).toBe(blobSpec.file.type);
      expect(row!.owner_id).toBe(blobSpec.expectedOwnerId);
    }, 60_000);
  });
}

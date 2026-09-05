import assert from "node:assert/strict";
import type { AnyUploader, Bucket } from "ocel/blob";
import { createUploadClient } from "ocel/blob/client";
import { type ContractContext, type ContractRow, json } from "../contract";

export const UPLOAD_ROW = "the upload protocol stores a document and /api/documents lists it";

async function createTodo(ctx: ContractContext, title: string) {
  const { res, body } = await json(ctx, "/api/todos", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ title }),
  });
  assert.equal(res.status, 201);
  return body as { id: number; title: string; done: boolean };
}

export const productRows: ContractRow[] = [
  {
    title: "POST /api/todos creates a todo and rejects a missing title",
    run: async (ctx) => {
      const todo = await createTodo(ctx, "write the journey harness");
      assert.equal(todo.title, "write the journey harness");
      assert.equal(todo.done, false);
      assert.equal(typeof todo.id, "number");

      const { res, body } = await json(ctx, "/api/todos", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({}),
      });
      assert.equal(res.status, 400);
      assert.equal(typeof (body as { error: string }).error, "string");
    },
  },
  {
    title: "GET /api/todos lists todos ordered by id",
    run: async (ctx) => {
      const first = await createTodo(ctx, "first");
      const second = await createTodo(ctx, "second");
      const { res, body } = await json(ctx, "/api/todos");
      assert.equal(res.status, 200);
      const rows = body as Array<{ id: number }>;
      const ids = rows.map((row) => row.id);
      assert.deepEqual([...ids].sort((a, b) => a - b), ids);
      assert.ok(ids.includes(first.id) && ids.includes(second.id));
    },
  },
  {
    title: "GET /api/todos/:id reads a todo and 404s for a missing one",
    run: async (ctx) => {
      const todo = await createTodo(ctx, "readable");
      const found = await json(ctx, `/api/todos/${todo.id}`);
      assert.equal(found.res.status, 200);
      assert.equal((found.body as { title: string }).title, "readable");

      const missing = await json(ctx, "/api/todos/99999999");
      assert.equal(missing.res.status, 404);
      assert.deepEqual(missing.body, { error: "not found" });
    },
  },
  {
    title: "DELETE /api/todos/:id deletes a todo and 404s for a missing one",
    run: async (ctx) => {
      const todo = await createTodo(ctx, "deletable");
      const deleted = await ctx.fetch(`${ctx.baseUrl}/api/todos/${todo.id}`, {
        method: "DELETE",
      });
      assert.equal(deleted.status, 204);

      const gone = await ctx.fetch(`${ctx.baseUrl}/api/todos/${todo.id}`);
      assert.equal(gone.status, 404);

      const again = await ctx.fetch(`${ctx.baseUrl}/api/todos/${todo.id}`, {
        method: "DELETE",
      });
      assert.equal(again.status, 404);
    },
  },
  {
    title: UPLOAD_ROW,
    run: async (ctx) => {
      const client = createUploadClient<Bucket<Record<string, AnyUploader>>>({
        url: `${ctx.baseUrl}/api/upload`,
        fetch: (url, init) => ctx.fetch(url, init as RequestInit),
        pollIntervalMs: 250,
        maxPollMs: 30_000,
      });
      const file = new File([new TextEncoder().encode("journey-bytes")], "report.pdf", {
        type: "application/pdf",
      });
      const result = await client.upload("document", {
        files: [file],
        input: { ownerId: "owner-1" },
      });
      assert.equal(result.files.length, 1);
      const key = result.files[0]!.key;
      assert.ok(key.startsWith("documents/"), `unexpected key ${key}`);

      const deadline = Date.now() + 30_000;
      for (;;) {
        const { body } = await json(ctx, "/api/documents");
        const row = (body as Array<{ key: string; name: string; mime_type: string; owner_id: string | null }>)
          .find((candidate) => candidate.key === key);
        if (row) {
          assert.equal(row.name, "report.pdf");
          assert.equal(row.mime_type, "application/pdf");
          assert.equal(row.owner_id, "owner-1");
          return;
        }
        assert.ok(Date.now() < deadline, `${key} never reached /api/documents`);
        await new Promise((resolve) => setTimeout(resolve, 250));
      }
    },
  },
  {
    title: "GET /api/documents answers with a list",
    run: async (ctx) => {
      const { res, body } = await json(ctx, "/api/documents");
      assert.equal(res.status, 200);
      assert.ok(Array.isArray(body));
    },
  },
];

export function migrates(rows: ContractRow[]): boolean {
  return rows.some((row) => productRows.includes(row));
}

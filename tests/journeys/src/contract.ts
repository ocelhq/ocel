import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import type { AnyUploader, Bucket } from "ocel/blob";
import { createUploadClient } from "ocel/blob/client";
import { nextCacheRows } from "./nextCache";
import { nextRoutingRows } from "./nextRouting";
import type { Leg, Suite } from "./spec";

export type Fetch = (input: string | URL | Request, init?: RequestInit) => Promise<Response>;

export const INITIAL_GREETING = "journey-hello";
export const REDEPLOY_GREETING = "redeployed";
export const SECRET_TOKEN = "journey-secret-never-in-a-body";
export const REDACTED = "<redacted>";

const OCEL_SVG_BYTES = 365;
const LARGE_BYTES = 5 * 1024 * 1024;
const SLEEP_MS = 30_000;

export type ContractContext = {
  app: string;
  baseUrl: string;
  greeting: string;
  leg: Leg;
  notes: Map<string, string>;
  fetch: Fetch;
};

export type ContractRow = {
  title: string;
  suite: Suite;
  run: (ctx: ContractContext) => Promise<void>;
};

export function redact(text: string): string {
  return text.split(SECRET_TOKEN).join(REDACTED);
}

function requestUrl(input: Parameters<Fetch>[0]): string {
  return input instanceof Request ? input.url : String(input);
}

export function secretGuarded(inner: Fetch): Fetch {
  return async (input, init) => {
    const res = await inner(input, init);
    const bytes = Buffer.from(await res.clone().arrayBuffer());
    assert.ok(
      !bytes.includes(SECRET_TOKEN),
      `${requestUrl(input)} leaked the secret value in its body`,
    );
    return res;
  };
}

async function json(ctx: ContractContext, path: string, init?: RequestInit) {
  const res = await ctx.fetch(`${ctx.baseUrl}${path}`, init);
  const text = await res.text();
  return { res, text, body: text.length > 0 ? JSON.parse(text) : undefined };
}

async function createTodo(ctx: ContractContext, title: string) {
  const { res, body } = await json(ctx, "/api/todos", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ title }),
  });
  assert.equal(res.status, 201);
  return body as { id: number; title: string; done: boolean };
}

export const contract: ContractRow[] = [
  {
    title: "GET /health answers with the app name",
    suite: "health",
    run: async (ctx) => {
      const { res, body } = await json(ctx, "/health");
      assert.equal(res.status, 200);
      assert.deepEqual(body, { ok: true, app: ctx.app });
    },
  },
  {
    title: "GET /ocel.svg serves the svg at its known length",
    suite: "static",
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/ocel.svg`);
      assert.equal(res.status, 200);
      assert.match(res.headers.get("content-type") ?? "", /^image\/svg\+xml/);
      const bytes = new Uint8Array(await res.arrayBuffer());
      assert.equal(bytes.byteLength, OCEL_SVG_BYTES);
    },
  },
  {
    title: "GET /missing.svg is a 404",
    suite: "static",
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/missing.svg`);
      assert.equal(res.status, 404);
    },
  },
  {
    title: "POST /api/todos creates a todo and rejects a missing title",
    suite: "product",
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
    suite: "product",
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
    suite: "product",
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
    suite: "product",
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
    title: "the upload protocol stores a document and /api/documents lists it",
    suite: "product",
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
    suite: "product",
    run: async (ctx) => {
      const { res, body } = await json(ctx, "/api/documents");
      assert.equal(res.status, 200);
      assert.ok(Array.isArray(body));
    },
  },
  {
    title: "GET /api/probes/env reports the greeting and never the secret",
    suite: "probes",
    run: async (ctx) => {
      const { res, body } = await json(ctx, "/api/probes/env");
      assert.equal(res.status, 200);
      const probe = body as { greeting: string; hasSecret: boolean; arch: string };
      assert.equal(probe.greeting, ctx.greeting);
      assert.equal(probe.hasSecret, true);
      assert.ok(probe.arch.length > 0);
    },
  },
  {
    title: "GET /api/probes/native answers from a native sqlite build",
    suite: "probes",
    run: async (ctx) => {
      const { res, body } = await json(ctx, "/api/probes/native");
      assert.equal(res.status, 200);
      const probe = body as { arch: string; sqlite: string; answer: number };
      assert.equal(probe.answer, 2);
      assert.ok(probe.arch.length > 0);
      assert.match(probe.sqlite, /^\d+\.\d+/);
    },
  },
  {
    title: "GET /api/probes/stream streams its chunks in order to the sentinel",
    suite: "probes",
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/api/probes/stream`);
      assert.equal(res.status, 200);
      assert.ok(res.body, "the stream carried no body");
      const chunks = (await res.text()).split("\n").filter((line) => line.length > 0);
      assert.deepEqual(chunks, [
        "ocel-stream-1",
        "ocel-stream-2",
        "ocel-stream-3",
        "ocel-stream-4",
        "ocel-stream-end",
      ]);
    },
  },
  {
    title: "GET /api/probes/status/:code passes every status through unchanged",
    suite: "probes",
    run: async (ctx) => {
      for (const code of [204, 302, 404, 418, 500, 503]) {
        const res = await ctx.fetch(`${ctx.baseUrl}/api/probes/status/${code}`, {
          redirect: "manual",
        });
        assert.equal(res.status, code, `status ${code} came back as ${res.status}`);
        if (code === 302) {
          assert.equal(res.headers.get("location"), "/api/probes/status/204");
        }
      }
    },
  },
  {
    title: "ALL /api/probes/echo echoes method, path, query, header and body",
    suite: "probes",
    run: async (ctx) => {
      for (const method of ["GET", "POST", "PUT", "PATCH", "DELETE"]) {
        const sent = method === "GET" ? undefined : JSON.stringify({ method });
        const res = await ctx.fetch(
          `${ctx.baseUrl}/api/probes/echo/deep/path?one=1&two=2`,
          {
            method,
            headers: {
              "x-ocel-probe": "probe-value",
              ...(sent ? { "content-type": "application/json" } : {}),
            },
            body: sent,
          },
        );
        assert.equal(res.status, 200);
        const echo = (await res.json()) as {
          method: string;
          path: string;
          query: Record<string, string>;
          header: string | null;
          body: unknown;
        };
        assert.equal(echo.method, method);
        assert.equal(echo.path, "/api/probes/echo/deep/path");
        assert.deepEqual(echo.query, { one: "1", two: "2" });
        assert.equal(echo.header, "probe-value");
        assert.deepEqual(echo.body, sent ? { method } : null);
      }
    },
  },
  {
    title: "POST /api/probes/large accepts a five megabyte body",
    suite: "probes",
    run: async (ctx) => {
      const body = randomBytes(LARGE_BYTES);
      const res = await ctx.fetch(`${ctx.baseUrl}/api/probes/large`, {
        method: "POST",
        headers: { "content-type": "application/octet-stream" },
        body,
      });
      assert.equal(res.status, 200);
      const probe = (await res.json()) as { bytes: number; sha256: string };
      assert.equal(probe.bytes, LARGE_BYTES);
      assert.equal(probe.sha256, createHash("sha256").update(body).digest("hex"));
    },
  },
  {
    title: "GET /api/probes/large returns five megabytes with a checksum",
    suite: "probes",
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/api/probes/large?bytes=${LARGE_BYTES}`);
      assert.equal(res.status, 200);
      const bytes = Buffer.from(await res.arrayBuffer());
      assert.equal(bytes.byteLength, LARGE_BYTES);
      assert.equal(
        createHash("sha256").update(bytes).digest("hex"),
        res.headers.get("x-ocel-sha256"),
      );
    },
  },
  {
    title: "GET /api/probes/sleep holds the request open for thirty seconds",
    suite: "probes",
    run: async (ctx) => {
      const { res, body } = await json(ctx, `/api/probes/sleep?ms=${SLEEP_MS}`);
      assert.equal(res.status, 200);
      assert.deepEqual(body, { slept: SLEEP_MS });
    },
  },
  {
    title: "GET /api/link answers with what it resolved and the greeting it deployed with",
    suite: "links",
    run: async (ctx) => {
      const { res, body } = await json(ctx, "/api/link");
      assert.equal(res.status, 200);
      const link = body as {
        host: string;
        port: number;
        database: string;
        hasPassword: boolean;
        greeting: string;
      };
      assert.ok(link.host.length > 0, "the app resolved no host");
      assert.equal(typeof link.port, "number");
      assert.ok(link.database.length > 0, "the app resolved no database");
      assert.equal(link.hasPassword, true);
      assert.equal(link.greeting, ctx.greeting);
    },
  },
  {
    title: "GET /api/link/query answers ok after a select through the link",
    suite: "links",
    run: async (ctx) => {
      const { res, body } = await json(ctx, "/api/link/query");
      assert.equal(res.status, 200);
      assert.deepEqual(body, { ok: true });
    },
  },
  ...nextRoutingRows,
  ...nextCacheRows,
];

export function contractRows(suites: Suite[]): ContractRow[] {
  return contract.filter((row) => suites.includes(row.suite));
}

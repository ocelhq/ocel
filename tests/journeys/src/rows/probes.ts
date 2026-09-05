import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { type ContractRow, json, LARGE_BYTES, SLEEP_MS } from "../contract";

export const STREAM_ROW = "GET /api/probes/stream streams its chunks in order to the sentinel";

export const probeRows: ContractRow[] = [
  {
    title: "GET /api/probes/env reports the greeting and never the secret",
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
    title: STREAM_ROW,
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
    run: async (ctx) => {
      const { res, body } = await json(ctx, `/api/probes/sleep?ms=${SLEEP_MS}`);
      assert.equal(res.status, 200);
      assert.deepEqual(body, { slept: SLEEP_MS });
    },
  },
];

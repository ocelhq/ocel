import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { isIP } from "node:net";
import { gzipSync } from "node:zlib";
import {
  type ContractContext,
  type ContractRow,
  describeResponse,
  json,
  LARGE_RESPONSE_BYTES,
  LONG_SLEEP_MS,
  SLEEP_MS,
} from "../contract";

export const STREAM_ROW = "GET /api/probes/stream streams its chunks in order to the sentinel";
export const SSE_GAP_ROW = "GET /api/probes/sse survives a forty-second silence between two events";
export const LONG_SLEEP_ROW = "GET /api/probes/sleep holds the request open for forty-five seconds";
export const RUNTIME_ROW = "GET /api/probes/runtime runs as production in UTC";
export const CLIENT_IP_ROW =
  "GET /api/probes/headers sees the real client address behind a forged one";
export const PUBLIC_ORIGIN_ROW = "GET /api/probes/headers is told the public scheme and host";

const EMPTY_BODY_BUDGET_MS = 5_000;
const SSE_GAP_MS = 40_000;
const SSE_SHORT_GAP_MS = 1_000;
const CHUNKED_BYTES = 1024 * 1024;
const ACCEPT_MATRIX_BYTES = 64 * 1024;
const MULTIPART_BYTES = 1024 * 1024;
const OVERSIZED_COOKIE_BYTES = 12 * 1024;
const OVERSIZED_URL_BYTES = 9 * 1024;
const FORGED_CLIENT = "203.0.113.9";
const REWRITTEN_QUERY = "?tag=a&tag=b&tag=a,b&x=1;2&y=%7B%7D&z=a%2Bb&w=100%25";
const REWRITTEN_ENTRIES = [
  ["tag", "a"],
  ["tag", "b"],
  ["tag", "a,b"],
  ["x", "1;2"],
  ["y", "{}"],
  ["z", "a+b"],
  ["w", "100%"],
];
const MALFORMED_QUERY = "?a=%%URL%%&b=%zz&c=100%";
const MALFORMED_ENTRIES = [
  ["a", "%%URL%%"],
  ["b", "%zz"],
  ["c", "100%"],
];
const CLOCK_DRIFT_MS = 5 * 60_000;

type Arrival = { at: number; text: string };

type Echo = {
  method: string;
  path: string;
  search: string;
  segments: string[];
  query: Record<string, unknown>;
  header: string | null;
  body: unknown;
};

type Dump = {
  headers: Record<string, string>;
  remote: string | null;
  ip: string | null;
  protocol: string;
  hostname: string;
};

function sha256(bytes: Uint8Array): string {
  return createHash("sha256").update(bytes).digest("hex");
}

async function arrivals(res: Response): Promise<Arrival[]> {
  assert.ok(res.body, "the stream carried no body");
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  const seen: Arrival[] = [];
  for (;;) {
    const { done, value } = await reader.read();
    if (done) {
      break;
    }
    seen.push({ at: Date.now(), text: decoder.decode(value, { stream: true }) });
  }
  return seen;
}

function distinctArrivals(seen: Arrival[]): number {
  return new Set(seen.map((one) => one.at)).size;
}

function spreadOf(seen: Arrival[]): number {
  const first = seen[0]?.at;
  const last = seen.at(-1)?.at;
  return first === undefined || last === undefined ? 0 : last - first;
}

function eventsOf(seen: Arrival[]): string[] {
  return seen
    .map((one) => one.text)
    .join("")
    .split("\n\n")
    .filter((block) => block.length > 0)
    .map((block) => block.split("\n").find((line) => line.startsWith("data: ")) ?? block)
    .map((line) => line.replace(/^data: /, "").replace(/ \d+$/, ""));
}

function isPrivate(address: string): boolean {
  return (
    /^(10\.|127\.|169\.254\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|0\.)/.test(address) ||
    /^(::1$|::$|fc|fd|fe[89ab])/i.test(address) ||
    /^::ffff:(10\.|127\.|169\.254\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/i.test(address)
  );
}

function forwardedChain(dump: Dump): string[] {
  return (dump.headers["x-forwarded-for"] ?? "")
    .split(",")
    .map((entry) => entry.trim())
    .filter((entry) => entry.length > 0);
}

async function dump(ctx: ContractContext, headers: Record<string, string>): Promise<Dump> {
  const { res, text, body } = await json(ctx, "/api/probes/headers", { headers });
  assert.equal(res.status, 200, describeResponse(res, text));
  return body as Dump;
}

async function largeWithAccept(ctx: ContractContext, accept: string | undefined) {
  const res = await ctx.fetch(`${ctx.baseUrl}/api/probes/large?bytes=${ACCEPT_MATRIX_BYTES}`, {
    headers: accept === undefined ? {} : { accept },
  });
  const label = `Accept: ${accept ?? "<absent>"}`;
  assert.equal(res.status, 200, label);
  assert.equal(res.headers.get("content-type"), "application/octet-stream", label);
  const bytes = Buffer.from(await res.arrayBuffer());
  assert.equal(bytes.byteLength, ACCEPT_MATRIX_BYTES, label);
  assert.equal(sha256(bytes), res.headers.get("x-ocel-sha256"), label);
}

function multipartFile(): { bytes: Buffer; sha256: string } {
  const every = Buffer.from(Array.from({ length: 256 }, (_, byte) => byte));
  const bytes = Buffer.alloc(MULTIPART_BYTES);
  for (let offset = 0; offset < bytes.byteLength; offset += every.byteLength) {
    every.copy(bytes, offset);
  }
  return { bytes, sha256: sha256(bytes) };
}

export const probeRows: ContractRow[] = [
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
      const seen = await arrivals(res);
      const chunks = seen
        .map((one) => one.text)
        .join("")
        .split("\n")
        .filter((line) => line.length > 0);
      assert.deepEqual(chunks, [
        "ocel-stream-1",
        "ocel-stream-2",
        "ocel-stream-3",
        "ocel-stream-4",
        "ocel-stream-end",
      ]);
      assert.ok(
        distinctArrivals(seen) >= 3,
        `the stream landed in ${distinctArrivals(seen)} arrival(s) over ${spreadOf(seen)}ms, so something buffered it`,
      );
    },
  },
  {
    title: "GET /api/probes/sse delivers each event as it is sent, uncompressed",
    run: async (ctx) => {
      const res = await ctx.fetch(
        `${ctx.baseUrl}/api/probes/sse?events=3&gap=${SSE_SHORT_GAP_MS}`,
        { headers: { accept: "text/event-stream", "accept-encoding": "gzip, br" } },
      );
      assert.equal(res.status, 200);
      assert.match(res.headers.get("content-type") ?? "", /^text\/event-stream/);
      assert.equal(res.headers.get("content-encoding"), null, "an event stream was compressed");
      const seen = await arrivals(res);
      assert.deepEqual(eventsOf(seen), ["ocel-sse-1", "ocel-sse-2", "ocel-sse-3", "ocel-sse-end"]);
      assert.ok(
        distinctArrivals(seen) >= 3 && spreadOf(seen) >= SSE_SHORT_GAP_MS,
        `three events a second apart landed in ${distinctArrivals(seen)} arrival(s) over ${spreadOf(seen)}ms`,
      );
    },
  },
  {
    title: SSE_GAP_ROW,
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/api/probes/sse?events=2&gap=${SSE_GAP_MS}`, {
        headers: { accept: "text/event-stream" },
      });
      assert.equal(res.status, 200);
      const seen = await arrivals(res);
      assert.deepEqual(eventsOf(seen), ["ocel-sse-1", "ocel-sse-2", "ocel-sse-end"]);
      assert.ok(
        spreadOf(seen) >= SSE_GAP_MS - 1_000,
        `both sides of a ${SSE_GAP_MS}ms gap landed within ${spreadOf(seen)}ms`,
      );
    },
  },
  {
    title: "GET /api/probes/status/:code passes every status through unchanged",
    run: async (ctx) => {
      for (const code of [204, 302, 404, 418, 500, 503]) {
        const startedAt = Date.now();
        const res = await ctx.fetch(`${ctx.baseUrl}/api/probes/status/${code}`, {
          redirect: "manual",
        });
        const text = await res.text();
        const elapsed = Date.now() - startedAt;
        assert.equal(res.status, code, `status ${code} came back as ${res.status}`);
        if (code === 302) {
          assert.equal(res.headers.get("location"), "/api/probes/status/204");
        }
        if (code === 204) {
          assert.equal(text, "", "a 204 carried a body");
          assert.ok(
            elapsed < EMPTY_BODY_BUDGET_MS,
            `an empty 204 took ${elapsed}ms to complete, so the edge waited for a body`,
          );
        }
      }
    },
  },
  {
    title: "ALL /api/probes/echo echoes method, path, query, header and body",
    run: async (ctx) => {
      for (const method of ["GET", "POST", "PUT", "PATCH", "DELETE"]) {
        const sent = method === "GET" ? undefined : JSON.stringify({ method });
        const res = await ctx.fetch(`${ctx.baseUrl}/api/probes/echo/deep/path?one=1&two=2`, {
          method,
          headers: {
            "x-ocel-probe": "probe-value",
            ...(sent ? { "content-type": "application/json" } : {}),
          },
          body: sent,
        });
        assert.equal(res.status, 200);
        const echo = (await res.json()) as Echo;
        assert.equal(echo.method, method);
        assert.equal(echo.path, "/api/probes/echo/deep/path");
        assert.equal(echo.search, "?one=1&two=2");
        assert.deepEqual(echo.segments, ["deep", "path"]);
        assert.deepEqual(echo.query, { one: "1", two: "2" });
        assert.equal(echo.header, "probe-value");
        assert.deepEqual(echo.body, sent ? { method } : null);
      }
    },
  },
  {
    title: "GET /api/probes/echo hands the app every query pair the edge likes to rewrite",
    run: async (ctx) => {
      const { res, text, body } = await json(ctx, `/api/probes/echo${REWRITTEN_QUERY}`);
      assert.equal(res.status, 200, describeResponse(res, text));
      const echo = body as Echo;
      assert.deepEqual(
        [...new URLSearchParams(echo.search)],
        REWRITTEN_ENTRIES,
        `the app was handed ${echo.search}`,
      );
    },
  },
  {
    title: "GET /api/probes/echo answers 200 to a query no decoder accepts",
    run: async (ctx) => {
      const { res, text, body } = await json(ctx, `/api/probes/echo${MALFORMED_QUERY}`);
      assert.equal(res.status, 200, describeResponse(res, text));
      const echo = body as Echo;
      assert.deepEqual(
        [...new URLSearchParams(echo.search)],
        MALFORMED_ENTRIES,
        `the app was handed ${echo.search}`,
      );
    },
  },
  {
    title: "GET /api/probes/echo decodes an encoded slash in a segment exactly once",
    run: async (ctx) => {
      const { res, text, body } = await json(ctx, "/api/probes/echo/a%2Fb%2Bc/caf%C3%A9");
      assert.equal(res.status, 200, describeResponse(res, text));
      const echo = body as Echo;
      assert.equal(echo.path, "/api/probes/echo/a%2Fb%2Bc/caf%C3%A9");
      assert.deepEqual(echo.segments, ["a/b+c", "café"]);
    },
  },
  {
    title:
      "GET /api/probes/echo takes oversized cookies and a nine-kilobyte URL without dropping the connection",
    run: async (ctx) => {
      const cookie = `ocel-fat=${"c".repeat(OVERSIZED_COOKIE_BYTES)}`;
      const fat = await ctx.fetch(`${ctx.baseUrl}/api/probes/echo`, { headers: { cookie } });
      await fat.text();
      assert.ok(
        fat.status === 200 || fat.status === 413 || fat.status === 431,
        `twelve kilobytes of cookie answered ${fat.status}`,
      );

      const pad = "u".repeat(OVERSIZED_URL_BYTES);
      const long = await ctx.fetch(`${ctx.baseUrl}/api/probes/echo?pad=${pad}`);
      const text = await long.text();
      assert.ok(
        long.status === 200 || long.status === 414,
        `a nine-kilobyte URL answered ${long.status}`,
      );
      if (long.status === 200) {
        assert.equal((JSON.parse(text) as Echo).query.pad, pad);
      }
    },
  },
  {
    title: "GET /api/probes/headers receives the request headers an edge likes to strip",
    run: async (ctx) => {
      const sent = {
        "accept-language": "fr-CA,fr;q=0.9,en;q=0.5",
        authorization: "Bearer journey-token-not-a-secret",
        referer: "https://ocel.test/from-here",
        "user-agent": "ocel-journey/1.0 (+https://ocel.test)",
        "x-ocel-probe": "probe-value",
      };
      const seen = await dump(ctx, sent);
      for (const [name, value] of Object.entries(sent)) {
        assert.equal(seen.headers[name], value, `${name} did not reach the app as sent`);
      }
    },
  },
  {
    title: "GET /api/probes/auth answers 401 with its WWW-Authenticate header under its own name",
    run: async (ctx) => {
      const { res, text } = await json(ctx, "/api/probes/auth");
      assert.equal(res.status, 401, describeResponse(res, text));
      assert.equal(res.headers.get("www-authenticate"), 'Bearer realm="ocel"');
      assert.equal(res.headers.get("x-ocel-number"), "42");
      const remapped = [...res.headers.keys()].filter((name) => name.includes("remapped"));
      assert.deepEqual(remapped, [], `the edge renamed ${remapped.join(", ")}`);
    },
  },
  {
    title: CLIENT_IP_ROW,
    run: async (ctx) => {
      const forged = await dump(ctx, { "x-forwarded-for": FORGED_CLIENT });
      const chain = forwardedChain(forged);
      const appended = chain.filter((entry) => entry !== FORGED_CLIENT);
      assert.ok(
        appended.length > 0,
        `no hop appended the client address to the forged chain "${chain.join(", ")}"`,
      );
      const client = appended[0]!;
      assert.ok(isIP(client) !== 0, `"${client}" is no address`);
      assert.ok(!isPrivate(client), `the client address the app sees is the private "${client}"`);
      assert.notEqual(forged.ip, FORGED_CLIENT, "the app trusted the forged client address");

      const honest = await dump(ctx, {});
      assert.equal(
        forwardedChain(honest)[0],
        client,
        "the client address changed between two requests from one runner",
      );
    },
  },
  {
    title: PUBLIC_ORIGIN_ROW,
    run: async (ctx) => {
      const seen = await dump(ctx, {});
      const expected = new URL(ctx.baseUrl);
      const scheme = seen.headers["x-forwarded-proto"] ?? seen.protocol;
      const host = seen.headers["x-forwarded-host"] ?? seen.headers.host;
      assert.equal(scheme, expected.protocol.replace(/:$/, ""));
      assert.equal(host, expected.host);
    },
  },
  {
    title: "PATCH /api/probes/method answers the app's own 405, not the edge's 403",
    run: async (ctx) => {
      const allowed = await json(ctx, "/api/probes/method");
      assert.equal(allowed.res.status, 200, describeResponse(allowed.res, allowed.text));

      const refused = await json(ctx, "/api/probes/method", { method: "PATCH" });
      assert.equal(refused.res.status, 405, describeResponse(refused.res, refused.text));
      assert.match(refused.res.headers.get("allow") ?? "", /\bGET\b/);
    },
  },
  {
    title: "OPTIONS /api/probes/cors carries exactly one Access-Control-Allow-Origin",
    run: async (ctx) => {
      const headers = {
        origin: "https://app.ocel.test",
        "access-control-request-method": "POST",
        "access-control-request-headers": "content-type",
      };
      const preflight = await ctx.fetch(`${ctx.baseUrl}/api/probes/cors`, {
        method: "OPTIONS",
        headers,
      });
      await preflight.text();
      assert.equal(preflight.status, 204);
      assert.equal(preflight.headers.get("access-control-allow-origin"), "*");
      assert.match(preflight.headers.get("access-control-allow-methods") ?? "", /\bPOST\b/);

      const actual = await ctx.fetch(`${ctx.baseUrl}/api/probes/cors`, {
        headers: { origin: headers.origin },
      });
      await actual.text();
      assert.equal(actual.status, 200);
      assert.equal(actual.headers.get("access-control-allow-origin"), "*");
    },
  },
  {
    title: "GET /api/probes/cookies delivers one and three Set-Cookie lines unfolded",
    run: async (ctx) => {
      for (const count of [1, 3]) {
        const res = await ctx.fetch(`${ctx.baseUrl}/api/probes/cookies?count=${count}`);
        await res.text();
        assert.equal(res.status, 200);
        const lines = res.headers.getSetCookie();
        assert.equal(lines.length, count, `${count} cookie(s) arrived as ${JSON.stringify(lines)}`);
        assert.deepEqual(
          lines.map((line) => line.split("=")[0]),
          Array.from({ length: count }, (_, i) => `ocel-cookie-${i + 1}`),
        );
      }
    },
  },
  {
    title: "GET /api/probes/compress is encoded exactly once for gzip and not at all for identity",
    run: async (ctx) => {
      const zipped = await ctx.fetch(`${ctx.baseUrl}/api/probes/compress`, {
        headers: { "accept-encoding": "gzip" },
      });
      const unzipped = await zipped.text();
      assert.equal(zipped.status, 200, describeResponse(zipped, unzipped));
      assert.equal(
        zipped.headers.get("content-encoding"),
        "gzip",
        `a gzip answer arrived encoded as ${zipped.headers.get("content-encoding")}`,
      );
      assert.equal(sha256(Buffer.from(unzipped, "utf8")), zipped.headers.get("x-ocel-sha256"));
      assert.equal((JSON.parse(unzipped) as { marker: string }).marker, "ocel-compress");

      const plain = await ctx.fetch(`${ctx.baseUrl}/api/probes/compress`, {
        headers: { "accept-encoding": "identity" },
      });
      const text = await plain.text();
      assert.equal(plain.status, 200, describeResponse(plain, text));
      assert.equal(plain.headers.get("content-encoding"), null);
      assert.equal(sha256(Buffer.from(text, "utf8")), plain.headers.get("x-ocel-sha256"));
    },
  },
  {
    title: "POST /api/probes/inflate reads a gzip request body as the bytes it compressed",
    run: async (ctx) => {
      const payload = Buffer.from(JSON.stringify({ filler: "ocel ".repeat(2048) }), "utf8");
      const res = await ctx.fetch(`${ctx.baseUrl}/api/probes/inflate`, {
        method: "POST",
        headers: { "content-type": "application/json", "content-encoding": "gzip" },
        body: gzipSync(payload),
      });
      const text = await res.text();
      assert.equal(res.status, 200, describeResponse(res, text));
      const probe = JSON.parse(text) as { encoding: string | null; bytes: number; sha256: string };
      assert.equal(probe.bytes, payload.byteLength);
      assert.equal(probe.sha256, sha256(payload));
    },
  },
  {
    title: "POST /api/probes/multipart keeps every byte value and a non-ASCII field",
    run: async (ctx) => {
      const file = multipartFile();
      const form = new FormData();
      form.append("note", "café ✓ 日本語");
      form.append(
        "upload",
        new Blob([file.bytes], { type: "application/octet-stream" }),
        "bytes.bin",
      );
      const res = await ctx.fetch(`${ctx.baseUrl}/api/probes/multipart`, {
        method: "POST",
        body: form,
      });
      const text = await res.text();
      assert.equal(res.status, 200, describeResponse(res, text));
      const probe = JSON.parse(text) as {
        fields: Record<string, string>;
        files: { field: string; name: string; bytes: number; sha256: string }[];
      };
      assert.equal(probe.fields.note, "café ✓ 日本語");
      assert.deepEqual(
        probe.files.map(({ field, name, bytes, sha256 }) => ({ field, name, bytes, sha256 })),
        [{ field: "upload", name: "bytes.bin", bytes: file.bytes.byteLength, sha256: file.sha256 }],
      );
    },
  },
  {
    title: "POST /api/probes/large round-trips a body the size the origin takes",
    run: async (ctx) => {
      const body = randomBytes(ctx.largeBodyBytes);
      const res = await ctx.fetch(`${ctx.baseUrl}/api/probes/large`, {
        method: "POST",
        headers: { "content-type": "application/octet-stream" },
        body,
      });
      const text = await res.text();
      assert.equal(res.status, 200, describeResponse(res, text));
      const probe = JSON.parse(text) as { bytes: number; sha256: string };
      assert.equal(probe.bytes, ctx.largeBodyBytes);
      assert.equal(probe.sha256, sha256(body));
    },
  },
  {
    title: "GET /api/probes/large returns five megabytes with a checksum",
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/api/probes/large?bytes=${LARGE_RESPONSE_BYTES}`);
      assert.equal(res.status, 200);
      const bytes = Buffer.from(await res.arrayBuffer());
      assert.equal(bytes.byteLength, LARGE_RESPONSE_BYTES);
      assert.equal(sha256(bytes), res.headers.get("x-ocel-sha256"));
    },
  },
  {
    title: "GET /api/probes/large is byte-identical whatever Accept says",
    run: async (ctx) => {
      for (const accept of ["image/*", "*/*", undefined]) {
        await largeWithAccept(ctx, accept);
      }
    },
  },
  {
    title: "GET /api/probes/large streamed without Content-Length arrives whole every time",
    run: async (ctx) => {
      for (let attempt = 1; attempt <= 3; attempt++) {
        const res = await ctx.fetch(
          `${ctx.baseUrl}/api/probes/large?bytes=${CHUNKED_BYTES}&chunked`,
        );
        assert.equal(res.status, 200, `attempt ${attempt}`);
        const bytes = Buffer.from(await res.arrayBuffer());
        assert.equal(bytes.byteLength, CHUNKED_BYTES, `attempt ${attempt} was cut short`);
        assert.equal(sha256(bytes), res.headers.get("x-ocel-sha256"), `attempt ${attempt}`);
      }
    },
  },
  {
    title: RUNTIME_ROW,
    run: async (ctx) => {
      const { res, text, body } = await json(ctx, "/api/probes/runtime");
      assert.equal(res.status, 200, describeResponse(res, text));
      const probe = body as {
        nodeEnv: string | null;
        tz: string | null;
        zone: string;
        offsetMinutes: number;
        now: string;
      };
      assert.equal(probe.nodeEnv, "production");
      assert.ok(probe.zone === "UTC" || probe.zone === "Etc/UTC", `the zone is ${probe.zone}`);
      assert.equal(probe.offsetMinutes, 0);
      assert.ok(
        Math.abs(Date.parse(probe.now) - Date.now()) < CLOCK_DRIFT_MS,
        `the app's clock says ${probe.now}`,
      );
    },
  },
  {
    title: "GET /api/probes/sleep holds the request open for twenty-five seconds",
    run: async (ctx) => {
      const { res, text, body } = await json(ctx, `/api/probes/sleep?ms=${SLEEP_MS}`);
      assert.equal(res.status, 200, describeResponse(res, text));
      assert.deepEqual(body, { slept: SLEEP_MS }, describeResponse(res, text));
    },
  },
  {
    title: LONG_SLEEP_ROW,
    run: async (ctx) => {
      const { res, text, body } = await json(ctx, `/api/probes/sleep?ms=${LONG_SLEEP_MS}`);
      assert.equal(res.status, 200, describeResponse(res, text));
      assert.deepEqual(body, { slept: LONG_SLEEP_MS }, describeResponse(res, text));
    },
  },
];

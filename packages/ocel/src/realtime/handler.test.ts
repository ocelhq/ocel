import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { bindingKey } from "../binding/binding.js";
import { BindingType } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import {
  appsyncBinding,
  appsyncHost,
  appsyncURL,
  type FakeGateway,
  readClaims,
  serveFakeGateway,
} from "../testing/realtime-gateway.js";

vi.mock("../runtime/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { realtime } = await import("./index.js");
const { createWebRealtimeHandler: createRealtimeHandler } = await import("./handler.js");

const OrderEvent = z.object({ status: z.string() });
const ChatMessage = z.object({ text: z.string().max(20) });

const ownedOrders = new Map([["o-1", "u1"]]);
const subscribeRule = vi.fn(
  async ({ auth, params }: { auth: { id: string }; params: { orderId: string } }) =>
    ownedOrders.get(params.orderId) === auth.id,
);
const publishRule = vi.fn(async ({ auth }: { auth: { id: string } }) => auth.id === "u1");

const rt = realtime("app", {
  authorize: async (request) => {
    const user = request.headers.get("x-user");
    return user ? { id: user } : null;
  },
  tokenTtl: "30s",
  channels: {
    "orders/:orderId": { schema: OrderEvent, subscribe: (context) => subscribeRule(context) },
    "projects/:projectId/deploys/:deployId": {
      schema: OrderEvent,
      wildcard: true,
      subscribe: async () => true,
    },
    "rooms/:roomId": {
      schema: ChatMessage,
      subscribe: async () => true,
      publish: (context) => publishRule(context),
    },
    status: { schema: OrderEvent, subscribe: "public" },
  },
});

const { GET, POST, OPTIONS } = createRealtimeHandler(rt);

function post(body: unknown, headers: Record<string, string> = {}) {
  return new Request("https://shop.example/api/realtime", {
    method: "POST",
    headers: { "content-type": "application/json", ...headers },
    body: typeof body === "string" ? body : JSON.stringify(body),
  });
}

interface Answer {
  transport: string;
  url: string;
  host?: string;
  connect?: { token: string; expiresAt: number };
  grants: { i: number; wire: string; token?: string }[];
  denied: { i: number; code: string }[];
}

async function answer(response: Response): Promise<Answer> {
  expect(response.status).toBe(200);
  return (await response.json()) as Answer;
}

describe("the realtime handler on AppSync Events", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv(bindingKey("app", BindingType.REALTIME), appsyncBinding);
    subscribeRule.mockClear();
    publishRule.mockClear();
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("names the transport, its url and host, and mints a connect token when asked", async () => {
    const res = await answer(await POST(post({ connect: true, ops: [] })));

    expect(res).toMatchObject({ transport: "appsync-events", url: appsyncURL, host: appsyncHost });
    const claims = readClaims(res.connect?.token ?? "");
    expect(claims).toMatchObject({
      iss: "ocel:rt:app",
      aud: appsyncHost,
      sub: "anonymous",
      ocel: { op: "connect", ch: "/app", ns: "app" },
    });
    expect(claims.exp).toBe(res.connect?.expiresAt);
    expect(Number(claims.exp) - Number(claims.iat)).toBe(30);
  });

  it("grants a public subscribe to anyone, with a token for its wire channel", async () => {
    const res = await answer(
      await POST(post({ ops: [{ op: "subscribe", pattern: "status", params: {} }] })),
    );

    expect(res.connect).toBeUndefined();
    expect(res.denied).toEqual([]);
    expect(res.grants).toEqual([{ i: 0, wire: "/app/status", token: expect.any(String) }]);
    expect(readClaims(res.grants[0]?.token ?? "")).toMatchObject({
      sub: "anonymous",
      ocel: { op: "subscribe", ch: "/app/status", ns: "app" },
    });
  });

  it("runs the subscribe rule with the auth, params and request, granting or denying each op", async () => {
    const res = await answer(
      await POST(
        post(
          {
            connect: true,
            ops: [
              { op: "subscribe", pattern: "orders/:orderId", params: { orderId: "o-1" } },
              { op: "subscribe", pattern: "orders/:orderId", params: { orderId: "o-2" } },
            ],
          },
          { "x-user": "u1" },
        ),
      ),
    );

    expect(res.grants).toEqual([{ i: 0, wire: "/app/orders/o-1", token: expect.any(String) }]);
    expect(res.denied).toEqual([{ i: 1, code: "forbidden" }]);
    expect(readClaims(res.connect?.token ?? "").sub).toBe("u1");
    expect(readClaims(res.grants[0]?.token ?? "").sub).toBe("u1");
    const [context] = subscribeRule.mock.calls[0] ?? [];
    expect(context).toMatchObject({ auth: { id: "u1" }, params: { orderId: "o-1" } });
    expect((context as unknown as { request: Request }).request.headers.get("x-user")).toBe("u1");
  });

  it("denies a ruled op to a caller authorize answered null for, without running the rule", async () => {
    const res = await answer(
      await POST(
        post({
          ops: [{ op: "subscribe", pattern: "orders/:orderId", params: { orderId: "o-1" } }],
        }),
      ),
    );

    expect(res.denied).toEqual([{ i: 0, code: "unauthenticated" }]);
    expect(subscribeRule).not.toHaveBeenCalled();
  });

  it("grants a wildcard subscribe leaving off trailing params as the prefix and /*", async () => {
    const res = await answer(
      await POST(
        post(
          {
            ops: [
              {
                op: "subscribe",
                pattern: "projects/:projectId/deploys/:deployId",
                params: { projectId: "p_1" },
              },
            ],
          },
          { "x-user": "u1" },
        ),
      ),
    );

    expect(res.grants).toEqual([
      { i: 0, wire: "/app/projects/0zobptc/deploys/*", token: expect.any(String) },
    ]);
  });

  it("denies each op it cannot serve with the code that says why", async () => {
    const res = await answer(
      await POST(
        post(
          {
            ops: [
              { op: "subscribe", pattern: "nope", params: {} },
              { op: "subscribe", pattern: "orders/:orderId", params: {} },
              { op: "subscribe", pattern: "orders/:orderId", params: { orderId: 7 } },
              { op: "subscribe", pattern: "orders/:orderId", params: { orderId: "x".repeat(31) } },
              { op: "unsubscribe", pattern: "status", params: {} },
              { op: "publish", pattern: "status", params: {}, body: { status: "up" } },
            ],
          },
          { "x-user": "u1" },
        ),
      ),
    );

    expect(res.grants).toEqual([]);
    expect(res.denied).toEqual([
      { i: 0, code: "unknown-pattern" },
      { i: 1, code: "missing-param" },
      { i: 2, code: "invalid-params" },
      { i: 3, code: "value-too-long" },
      { i: 4, code: "unknown-op" },
      { i: 5, code: "no-publish-rule" },
    ]);
  });

  it("denies an op whose rule throws, and still serves the rest", async () => {
    subscribeRule.mockRejectedValueOnce(new Error("db down"));

    const res = await answer(
      await POST(
        post(
          {
            ops: [
              { op: "subscribe", pattern: "orders/:orderId", params: { orderId: "o-1" } },
              { op: "subscribe", pattern: "status", params: {} },
            ],
          },
          { "x-user": "u1" },
        ),
      ),
    );

    expect(res.denied).toEqual([{ i: 0, code: "rule-error" }]);
    expect(res.grants).toEqual([{ i: 1, wire: "/app/status", token: expect.any(String) }]);
  });

  it("denies each malformed op on its own, serving the rest", async () => {
    const res = await answer(
      await POST(
        post({
          ops: [
            5,
            null,
            [],
            { op: "subscribe", pattern: "status", extra: 1 },
            { Op: "subscribe", pattern: "status" },
            { op: "subscribe", pattern: "status", body: {} },
            { pattern: "status" },
            { op: "Subscribe", pattern: "status" },
            { op: "subscribe" },
            { op: "subscribe", pattern: 7 },
            { op: "subscribe", pattern: "status", params: null },
            { op: "subscribe", pattern: "status", params: [] },
            { op: "subscribe", pattern: "status", params: "x" },
          ],
        }),
      ),
    );

    expect(res.denied).toEqual([
      { i: 0, code: "invalid-op" },
      { i: 1, code: "invalid-op" },
      { i: 2, code: "invalid-op" },
      { i: 3, code: "invalid-op" },
      { i: 4, code: "invalid-op" },
      { i: 5, code: "invalid-op" },
      { i: 6, code: "unknown-op" },
      { i: 7, code: "unknown-op" },
      { i: 8, code: "unknown-pattern" },
      { i: 9, code: "unknown-pattern" },
      { i: 11, code: "invalid-params" },
      { i: 12, code: "invalid-params" },
    ]);
    expect(res.grants).toEqual([{ i: 10, wire: "/app/status", token: expect.any(String) }]);
  });
});

describe("the realtime handler's defaults", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv(bindingKey("app", BindingType.REALTIME), appsyncBinding);
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("answers every response uncacheable and sets no cookie", async () => {
    const res = await POST(post({ connect: true, ops: [] }));

    expect(res.headers.get("cache-control")).toBe("no-store");
    expect(res.headers.get("set-cookie")).toBeNull();
  });

  it("refuses GET with 405, naming POST as allowed", async () => {
    const res = await GET(new Request("https://shop.example/api/realtime"));

    expect(res.status).toBe(405);
    expect(res.headers.get("allow")).toBe("POST");
    expect(res.headers.get("cache-control")).toBe("no-store");
  });

  it("refuses a body that is not application/json with 415", async () => {
    const res = await POST(post("{}", { "content-type": "text/plain" }));

    expect(res.status).toBe(415);
  });

  it("refuses a body that is no batch with 400", async () => {
    for (const body of [
      "{",
      "[]",
      { ops: "x" },
      {},
      { connect: null, ops: [] },
      { connect: "yes", ops: [] },
      { Ops: [] },
      { ops: [], extra: 1 },
    ]) {
      const res = await POST(post(body));

      expect(res.status, JSON.stringify(body)).toBe(400);
      expect(res.headers.get("cache-control")).toBe("no-store");
    }
  });

  it("refuses a body over 1 MiB with 413 while reading it, without waiting for its end", async () => {
    const chunk = new Uint8Array(64 * 1024).fill(32);
    const endless = new ReadableStream<Uint8Array>({
      pull(controller) {
        controller.enqueue(chunk);
      },
    });

    const res = await POST(
      new Request("https://shop.example/api/realtime", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: endless,
        duplex: "half",
      } as RequestInit),
    );

    expect(res.status).toBe(413);
    expect(res.headers.get("cache-control")).toBe("no-store");
  });

  it("hands authorize and every rule a request whose body is still readable", async () => {
    const read: unknown[] = [];
    const reading = realtime("app", {
      authorize: async (request) => {
        read.push(await request.json());
        return { id: "u1" };
      },
      channels: {
        status: {
          schema: OrderEvent,
          subscribe: async ({ request }) => {
            read.push(await request.json());
            return true;
          },
        },
      },
    });
    const batch = {
      ops: [
        { op: "subscribe", pattern: "status" },
        { op: "subscribe", pattern: "status" },
      ],
    };

    const res = await answer(await createRealtimeHandler(reading).POST(post(batch)));

    expect(res.grants).toHaveLength(2);
    expect(read).toEqual([batch, batch, batch]);
  });

  it("refuses more than 50 ops in one request with 400", async () => {
    const ops = Array.from({ length: 51 }, () => ({ op: "subscribe", pattern: "status" }));

    const res = await POST(post({ ops }));

    expect(res.status).toBe(400);
  });

  it("serves its own origin, and refuses another with 403 and no CORS headers", async () => {
    const same = await POST(post({ ops: [] }, { origin: "https://shop.example" }));
    const other = await POST(post({ ops: [] }, { origin: "https://evil.example" }));

    expect(same.status).toBe(200);
    expect(same.headers.get("access-control-allow-origin")).toBeNull();
    expect(other.status).toBe(403);
    expect(other.headers.get("access-control-allow-origin")).toBeNull();
  });

  it("refuses its own host under another scheme, an opaque origin and one with no host, with 403", async () => {
    for (const origin of ["http://shop.example", "null", "file:///etc/passwd", "not a url"]) {
      const res = await POST(post({ ops: [] }, { origin }));

      expect(res.status, origin).toBe(403);
      expect(res.headers.get("cache-control")).toBe("no-store");
    }
  });

  it("judges its own scheme by the first protocol a proxy forwarded", async () => {
    const request = (proto: string) =>
      new Request("http://10.0.0.5:3000/api/realtime", {
        method: "POST",
        headers: {
          "content-type": "application/json",
          origin: "https://shop.example",
          host: "shop.example",
          "x-forwarded-proto": proto,
        },
        body: JSON.stringify({ ops: [] }),
      });

    expect((await POST(request(" HTTPS , http"))).status).toBe(200);
    expect((await POST(request("http"))).status).toBe(403);
  });

  it("judges its own origin by the host a proxy forwarded", async () => {
    const res = await POST(
      new Request("http://10.0.0.5:3000/api/realtime", {
        method: "POST",
        headers: {
          "content-type": "application/json",
          origin: "https://shop.example",
          "x-forwarded-host": "shop.example",
          "x-forwarded-proto": "https",
        },
        body: JSON.stringify({ ops: [] }),
      }),
    );

    expect(res.status).toBe(200);
  });

  it("serves an allowed origin with CORS headers, and answers its preflight", async () => {
    const cors = createRealtimeHandler(rt, { allowedOrigins: ["https://app.example"] });

    const res = await cors.POST(post({ ops: [] }, { origin: "https://app.example" }));
    const preflight = await cors.OPTIONS(
      new Request("https://shop.example/api/realtime", {
        method: "OPTIONS",
        headers: { origin: "https://app.example", "access-control-request-method": "POST" },
      }),
    );
    const refused = await cors.POST(post({ ops: [] }, { origin: "https://evil.example" }));

    expect(res.status).toBe(200);
    expect(res.headers.get("access-control-allow-origin")).toBe("https://app.example");
    expect(res.headers.get("vary")).toContain("Origin");
    expect(preflight.status).toBe(204);
    expect(preflight.headers.get("access-control-allow-origin")).toBe("https://app.example");
    expect(preflight.headers.get("access-control-allow-methods")).toBe("POST");
    expect(preflight.headers.get("access-control-allow-headers")).toBe(
      "authorization, content-type",
    );
    expect(refused.status).toBe(403);
  });

  it("answers no preflight when no origin is allowed beyond its own", async () => {
    const res = await OPTIONS(
      new Request("https://shop.example/api/realtime", {
        method: "OPTIONS",
        headers: { origin: "https://app.example" },
      }),
    );

    expect(res.status).toBe(405);
    expect(res.headers.get("access-control-allow-origin")).toBeNull();
  });

  it.each([
    ["no binding was delivered", undefined],
    [
      "the binding names a transport this SDK does not speak",
      JSON.stringify({
        name: "realtime--app",
        realtime: {
          ...JSON.parse(appsyncBinding).realtime,
          transport: "REALTIME_TRANSPORT_UNSPECIFIED",
        },
      }),
    ],
  ])("answers 500 no-store when %s", async (_, binding) => {
    vi.stubEnv(bindingKey("app", BindingType.REALTIME), binding);

    const res = await POST(post({ connect: true, ops: [{ op: "subscribe", pattern: "status" }] }));

    expect(res.status).toBe(500);
    expect(res.headers.get("cache-control")).toBe("no-store");
    expect(await res.json()).toEqual({ error: expect.any(String) });
  });

  it.each([
    ["a connect token", { connect: true, ops: [] }],
    ["a subscribe token", { ops: [{ op: "subscribe", pattern: "status" }] }],
  ])("answers 500 no-store when %s cannot be minted", async (_, batch) => {
    vi.stubEnv(
      bindingKey("app", BindingType.REALTIME),
      JSON.stringify({
        name: "realtime--app",
        realtime: { ...JSON.parse(appsyncBinding).realtime, signingKey: "AAAA" },
      }),
    );

    const res = await POST(post(batch));

    expect(res.status).toBe(500);
    expect(res.headers.get("cache-control")).toBe("no-store");
  });

  it("answers 500 without the cause when authorize throws", async () => {
    const failing = realtime("app", {
      authorize: async () => {
        throw new Error("secret detail");
      },
      channels: { status: { schema: OrderEvent, subscribe: "public" } },
    });

    const res = await createRealtimeHandler(failing).POST(post({ ops: [] }));

    expect(res.status).toBe(500);
    expect(await res.text()).not.toContain("secret detail");
  });
});

describe("a publish relayed through the realtime handler", () => {
  let gateway: FakeGateway;

  beforeEach(async () => {
    gateway = await serveFakeGateway();
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv(bindingKey("app", BindingType.REALTIME), gateway.binding);
    publishRule.mockClear();
  });

  afterEach(async () => {
    vi.unstubAllEnvs();
    await gateway.close();
  });

  it("runs the publish rule with the parsed body, then publishes the event from the server", async () => {
    const res = await answer(
      await POST(
        post(
          {
            ops: [
              {
                op: "publish",
                pattern: "rooms/:roomId",
                params: { roomId: "r1" },
                body: { text: "hi" },
              },
            ],
          },
          { "x-user": "u1" },
        ),
      ),
    );

    expect(res.transport).toBe("ocel-gateway");
    expect(res.host).toBeUndefined();
    expect(res.grants).toEqual([{ i: 0, wire: "/app/rooms/r1" }]);
    expect(publishRule.mock.calls[0]?.[0]).toMatchObject({
      auth: { id: "u1" },
      params: { roomId: "r1" },
      body: { text: "hi" },
    });
    expect(gateway.published).toHaveLength(1);
    expect(gateway.published[0]?.envelope).toMatchObject({
      v: 1,
      ch: "/app/rooms/r1",
      kind: "live",
      data: { text: "hi" },
    });
  });

  it("denies a publish its rule refuses or its schema rejects, publishing nothing", async () => {
    const res = await answer(
      await POST(
        post(
          {
            ops: [
              {
                op: "publish",
                pattern: "rooms/:roomId",
                params: { roomId: "r1" },
                body: { text: 1 },
              },
            ],
          },
          { "x-user": "u1" },
        ),
      ),
    );
    const forbidden = await answer(
      await POST(
        post(
          {
            ops: [
              {
                op: "publish",
                pattern: "rooms/:roomId",
                params: { roomId: "r1" },
                body: { text: "x" },
              },
            ],
          },
          { "x-user": "u2" },
        ),
      ),
    );

    expect(res.denied).toEqual([{ i: 0, code: "invalid-body" }]);
    expect(forbidden.denied).toEqual([{ i: 0, code: "forbidden" }]);
    expect(gateway.published).toEqual([]);
  });

  it("answers 500 no-store when the token to publish a relayed event cannot be minted", async () => {
    vi.stubEnv(
      bindingKey("app", BindingType.REALTIME),
      JSON.stringify({
        name: "realtime--app",
        realtime: { ...JSON.parse(gateway.binding).realtime, signingKey: "AAAA" },
      }),
    );

    const res = await POST(
      post(
        {
          ops: [
            {
              op: "publish",
              pattern: "rooms/:roomId",
              params: { roomId: "r1" },
              body: { text: "hi" },
            },
          ],
        },
        { "x-user": "u1" },
      ),
    );

    expect(res.status).toBe(500);
    expect(res.headers.get("cache-control")).toBe("no-store");
    expect(gateway.published).toEqual([]);
  });

  it("denies a publish the transport refuses", async () => {
    await gateway.close();
    gateway = await serveFakeGateway({ status: 401 });
    vi.stubEnv(bindingKey("app", BindingType.REALTIME), gateway.binding);

    const res = await answer(
      await POST(
        post(
          {
            ops: [
              {
                op: "publish",
                pattern: "rooms/:roomId",
                params: { roomId: "r1" },
                body: { text: "hi" },
              },
            ],
          },
          { "x-user": "u1" },
        ),
      ),
    );

    expect(res.denied).toEqual([{ i: 0, code: "publish-failed" }]);
  });

  it("denies a publish with the first code that applies, checking params before the body and the body before auth", async () => {
    const op = (fields: Record<string, unknown>) => ({
      op: "publish",
      pattern: "rooms/:roomId",
      params: { roomId: "r1" },
      body: { text: "hi" },
      ...fields,
    });

    const res = await answer(
      await POST(
        post({
          ops: [
            op({ pattern: "status", params: 7 }),
            op({ params: 7, body: { text: 1 } }),
            op({ params: {}, body: { text: 1 } }),
            { op: "publish", pattern: "rooms/:roomId", params: { roomId: "r1" } },
            op({ body: { text: 1 } }),
            op({}),
          ],
        }),
      ),
    );

    expect(res.denied).toEqual([
      { i: 0, code: "no-publish-rule" },
      { i: 1, code: "invalid-params" },
      { i: 2, code: "missing-param" },
      { i: 3, code: "invalid-body" },
      { i: 4, code: "invalid-body" },
      { i: 5, code: "unauthenticated" },
    ]);
    expect(publishRule).not.toHaveBeenCalled();
    expect(gateway.published).toEqual([]);
  });

  it("publishes relayed events in batch order, each after the one before it finished", async () => {
    publishRule.mockImplementationOnce(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
      return true;
    });
    const op = (text: string) => ({
      op: "publish",
      pattern: "rooms/:roomId",
      params: { roomId: "r1" },
      body: { text },
    });

    const res = await answer(
      await POST(post({ ops: [op("first"), op("second"), op("third")] }, { "x-user": "u1" })),
    );

    expect(res.grants.map((grant) => grant.i)).toEqual([0, 1, 2]);
    expect(gateway.published.map((event) => event.envelope.data)).toEqual([
      { text: "first" },
      { text: "second" },
      { text: "third" },
    ]);
  });
});

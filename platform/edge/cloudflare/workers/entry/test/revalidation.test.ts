import { refreshHeader } from "@framework/next-cache";
import { parseMessage } from "@platform/edge-contract/revalidation";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  enqueueTimeoutMs,
  type RevalidationMessage,
  type RevalidationRoute,
  type RevalidationSender,
  revalidationIds,
  revalidationMessage,
  revalidationRetryWindowMs,
  revalidationSender,
} from "../src/revalidation";

const route: RevalidationRoute = {
  headers: {
    "x-prerender-revalidate": "TOKEN",
    "x-ocel-entry": "app/blog/page",
    "x-forwarded-host": "app.example",
    "x-forwarded-proto": "https",
  },
  expect: null,
  isrPrefix: "prod/p1/web/build-1",
  routeId: "/blog",
  routePath: "/blog",
  origin: "https://r1-abc.o.example.com",
};

const queueUrl = "https://sqs.eu-west-2.amazonaws.com/363236815301/ocel-revalidate.fifo";

async function capture(
  send: () => Promise<boolean>,
  respond: (request: Request) => Promise<Response> | Response = () =>
    new Response("<SendMessageResponse/>"),
): Promise<{ accepted: boolean; request: Request | undefined }> {
  let captured: Request | undefined;
  const original = globalThis.fetch;
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    captured = new Request(input as RequestInfo, init);
    return respond(captured);
  }) as typeof fetch;
  try {
    const accepted = await send();
    return { accepted, request: captured };
  } finally {
    globalThis.fetch = original;
  }
}

const sqsEnv = {
  OCEL_REVALIDATE_QUEUE_URL: queueUrl,
  OCEL_EDGE_ACCESS_KEY_ID: "AKIAEXAMPLE",
  OCEL_EDGE_SECRET_KEY: "secretkey",
};

const sender = (over: { timeoutMs?: number } = {}) =>
  sendWithOrigin(revalidationSender(sqsEnv, over.timeoutMs)!);

function sendWithOrigin(
  send: RevalidationSender,
): (message: RevalidationMessage) => Promise<boolean> {
  return (message) => send(message, route.origin);
}

function edgeQueue(send: (body: string, options?: { contentType?: string }) => Promise<void>) {
  return { send: vi.fn(send) } as unknown as Queue & { send: ReturnType<typeof vi.fn> };
}

const body = async (request: Request) => new URLSearchParams(await request.text());

const warnings = () => vi.spyOn(console, "warn").mockImplementation(() => {});
const logged = (warn: ReturnType<typeof warnings>) => JSON.stringify(warn.mock.calls);

afterEach(() => {
  vi.restoreAllMocks();
});

describe("revalidationIds", () => {
  it("derives the same dedup id for the same route and entry generation", async () => {
    const first = await revalidationIds(revalidationMessage(route, 1_000, 10));
    const second = await revalidationIds(revalidationMessage(route, 1_000, 99));

    expect(first.MessageDeduplicationId).toBe(second.MessageDeduplicationId);
    expect(first.MessageDeduplicationId).toMatch(/^[0-9a-f]{64}$/);
  });

  it("lets an entry that is still stale be re-enqueued a retry window later", async () => {
    const first = await revalidationIds(revalidationMessage(route, 1_000, 10));
    const later = await revalidationIds(
      revalidationMessage(route, 1_000, 10 + revalidationRetryWindowMs),
    );

    expect(later.MessageDeduplicationId).not.toBe(first.MessageDeduplicationId);
  });

  it("derives a different dedup id once lastModified moves", async () => {
    const before = await revalidationIds(revalidationMessage(route, 1_000, 10));
    const after = await revalidationIds(revalidationMessage(route, 2_000, 10));

    expect(after.MessageDeduplicationId).not.toBe(before.MessageDeduplicationId);
  });

  it("derives a different dedup id for a different route in the same deploy", async () => {
    const blog = await revalidationIds(revalidationMessage(route, 1_000, 10));
    const other = await revalidationIds(
      revalidationMessage({ ...route, routePath: "/docs" }, 1_000, 10),
    );

    expect(other.MessageDeduplicationId).not.toBe(blog.MessageDeduplicationId);
  });

  it("groups by deploy and route", async () => {
    const ids = await revalidationIds(revalidationMessage(route, 1_000, 10));

    expect(ids.MessageGroupId).toBe("prod/p1/web/build-1:/blog");
  });

  it("keeps the group id inside SQS's 128 characters on a pathological route", async () => {
    const long = `/${"segment/".repeat(40)}end`;
    const ids = await revalidationIds(
      revalidationMessage({ ...route, routePath: long }, 1_000, 10),
    );

    expect(long.length).toBeGreaterThan(128);
    expect(ids.MessageGroupId.length).toBeLessThanOrEqual(128);
  });

  it("keeps two pathological routes sharing a prefix in different groups", async () => {
    const shared = `/${"segment/".repeat(40)}`;
    const one = await revalidationIds(
      revalidationMessage({ ...route, routePath: `${shared}a` }, 1_000, 10),
    );
    const two = await revalidationIds(
      revalidationMessage({ ...route, routePath: `${shared}b` }, 1_000, 10),
    );

    expect(one.MessageGroupId).not.toBe(two.MessageGroupId);
  });
});

describe("revalidationMessage", () => {
  it("includes the route, the entry generation and the enqueue time", () => {
    expect(revalidationMessage(route, 1_000, 42)).toEqual({
      v: 1,
      headers: { ...route.headers, [refreshHeader]: "1000" },
      expect: null,
      isrPrefix: "prod/p1/web/build-1",
      routeId: "/blog",
      routePath: "/blog",
      lastModified: 1_000,
      enqueuedAt: 42,
    });
  });
});

describe("the queue send", () => {
  it("signs the SendMessage against sqs in the queue's own region", async () => {
    const { accepted, request } = await capture(() =>
      sender()(revalidationMessage(route, 1_000, 42)),
    );

    expect(accepted).toBe(true);
    const auth = request?.headers.get("authorization") ?? "";
    expect(auth).toContain("AWS4-HMAC-SHA256");
    expect(auth).toContain("/eu-west-2/sqs/aws4_request");
    expect(request?.url).toBe(queueUrl);
    expect(request?.method).toBe("POST");
  });

  it("sends the message under both FIFO ids", async () => {
    const message = revalidationMessage(route, 1_000, 42);
    const { request } = await capture(() => sender()(message));
    const sent = await body(request!);
    const ids = await revalidationIds(message);

    expect(sent.get("Action")).toBe("SendMessage");
    expect(sent.get("MessageGroupId")).toBe(ids.MessageGroupId);
    expect(sent.get("MessageDeduplicationId")).toBe(ids.MessageDeduplicationId);
    expect(JSON.parse(sent.get("MessageBody")!)).toEqual(message);
  });

  it("reports refusal on a non-2xx, so the caller renders instead", async () => {
    const warn = warnings();
    const { accepted } = await capture(
      () => sender()(revalidationMessage(route, 1_000, 42)),
      () => new Response("AccessDenied", { status: 403 }),
    );

    expect(accepted).toBe(false);
    expect(warn).toHaveBeenCalledTimes(1);
    expect(logged(warn)).toContain("403");
  });

  it("names the status and nothing else when the queue refuses", async () => {
    const warn = warnings();
    await capture(
      () => sender()(revalidationMessage(route, 1_000, 42)),
      () => new Response("<Error><Code>AccessDenied</Code></Error>", { status: 403 }),
    );

    expect(logged(warn)).not.toContain("TOKEN");
    expect(logged(warn)).not.toContain("AccessDenied");
  });

  it("reports refusal when the send throws", async () => {
    const warn = warnings();
    const { accepted } = await capture(
      () => sender()(revalidationMessage(route, 1_000, 42)),
      () => {
        throw new Error("network");
      },
    );

    expect(accepted).toBe(false);
    expect(warn).toHaveBeenCalledTimes(1);
    expect(logged(warn)).not.toContain("TOKEN");
  });

  it("reports refusal when the send outlives its budget", async () => {
    const warn = warnings();
    const { accepted } = await capture(
      () => sender({ timeoutMs: 5 })(revalidationMessage(route, 1_000, 42)),
      (request) =>
        new Promise<Response>((_, reject) => {
          request.signal.addEventListener("abort", () => reject(request.signal.reason));
        }),
    );

    expect(accepted).toBe(false);
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("says nothing at all about a send the queue took", async () => {
    const warn = warnings();
    const { accepted } = await capture(() => sender()(revalidationMessage(route, 1_000, 42)));

    expect(accepted).toBe(true);
    expect(warn).not.toHaveBeenCalled();
  });

  it("bounds every send by one second", () => {
    expect(enqueueTimeoutMs).toBe(1_000);
  });
});

describe("revalidationSender", () => {
  it("is built when the queue URL and both edge credentials are bound", () => {
    expect(sender()).toBeDefined();
  });

  it.each([
    ["no queue URL", { ...sqsEnv, OCEL_REVALIDATE_QUEUE_URL: undefined }],
    ["no access key", { ...sqsEnv, OCEL_EDGE_ACCESS_KEY_ID: undefined }],
    ["no secret key", { ...sqsEnv, OCEL_EDGE_SECRET_KEY: undefined }],
    ["nothing bound", {}],
  ])("is absent with %s, leaving every refresh rendering as before", (_name, env) => {
    expect(revalidationSender(env)).toBeUndefined();
  });

  it("is absent when the queue URL names no region it could sign against", () => {
    expect(
      revalidationSender({
        ...sqsEnv,
        OCEL_REVALIDATE_QUEUE_URL: "https://queue.example.com/q.fifo",
      }),
    ).toBeUndefined();
  });

  it("prefers the edge's queue over SQS when both are bound", async () => {
    const queue = edgeQueue(async () => {});
    const send = revalidationSender({ ...sqsEnv, OCEL_REFRESH_QUEUE: queue })!;

    const { accepted, request } = await capture(() =>
      send(revalidationMessage(route, 1_000, 42), route.origin),
    );

    expect(accepted).toBe(true);
    expect(queue.send).toHaveBeenCalledTimes(1);
    expect(request).toBeUndefined();
  });

  it("sends to the queue it was built for", async () => {
    const { accepted, request } = await capture(() =>
      sender()(revalidationMessage(route, 1_000, 42)),
    );

    expect(accepted).toBe(true);
    expect(request?.url).toBe(queueUrl);
  });
});

describe("the edge's refresh queue", () => {
  const send = (queue: Queue, timeoutMs?: number) =>
    revalidationSender({ OCEL_REFRESH_QUEUE: queue }, timeoutMs)!;

  it("sends a refresh to the edge's queue as text naming the origin it renders at", async () => {
    const queue = edgeQueue(async () => {});
    const message = revalidationMessage(route, 1_000, 42);

    expect(await send(queue)(message, route.origin)).toBe(true);

    expect(queue.send).toHaveBeenCalledTimes(1);
    const [body, options] = queue.send.mock.calls[0]!;
    expect(options).toEqual({ contentType: "text" });
    expect(typeof body).toBe("string");
    expect(JSON.parse(body)).toEqual({ ...message, origin: "https://r1-abc.o.example.com" });
    expect(JSON.parse(body).headers[refreshHeader]).toBe("1000");
  });

  it("builds a queued refresh the refresher's own parser accepts", async () => {
    const queue = edgeQueue(async () => {});
    await send(queue)(revalidationMessage(route, 1_000, 42), route.origin);

    const body = queue.send.mock.calls[0]![0] as string;
    const parsed = parseMessage(body);
    expect(parsed.ok).toBe(true);
    const origin = (JSON.parse(body) as { origin: string }).origin;
    expect(new URL("/blog", origin).href).toBe("https://r1-abc.o.example.com/blog");
  });

  it("reports refusal when the edge's queue rejects the send", async () => {
    const warn = warnings();
    const queue = edgeQueue(async () => {
      throw new Error("queue unreachable");
    });

    expect(await send(queue)(revalidationMessage(route, 1_000, 42), route.origin)).toBe(false);
    expect(warn).toHaveBeenCalledTimes(1);
    expect(logged(warn)).not.toContain("TOKEN");
  });

  it("reports refusal when the edge's queue outlives its budget", async () => {
    const warn = warnings();
    const queue = edgeQueue(() => new Promise<void>(() => {}));

    expect(await send(queue, 5)(revalidationMessage(route, 1_000, 42), route.origin)).toBe(false);
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("sends the same SQS message as before", async () => {
    const message = revalidationMessage(route, 1_000, 42);
    const { request } = await capture(() => sender()(message));

    const sent = JSON.parse((await body(request!)).get("MessageBody")!);
    expect(sent).toEqual(message);
    expect(sent).not.toHaveProperty("origin");
  });
});

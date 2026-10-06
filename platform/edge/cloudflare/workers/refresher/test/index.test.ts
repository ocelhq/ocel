import { afterEach, describe, expect, it, vi } from "vitest";

import type { Env } from "../src/env";
import worker from "../src/index";

const ORIGIN = "https://d1.origin.example";

function body(overrides: Record<string, unknown> = {}): string {
  return JSON.stringify({
    v: 1,
    headers: { "x-ocel-refresh": "1700000000000", "x-secret-header": "secret-header-value" },
    expect: { header: "x-ocel-rendered", value: "1700000000000" },
    isrPrefix: "prod/proj/web/BUILD1",
    routeId: "blog",
    routePath: "/blog",
    lastModified: 1700000000000,
    enqueuedAt: 1700000001000,
    origin: ORIGIN,
    ...overrides,
  });
}

interface Settled {
  acks: number;
  retries: { delaySeconds?: number }[];
}

function message(raw: unknown, attempts = 1) {
  const settled: Settled = { acks: 0, retries: [] };
  return {
    settled,
    message: {
      id: "m1",
      timestamp: new Date(),
      body: raw,
      attempts,
      ack: () => {
        settled.acks++;
      },
      retry: (options?: { delaySeconds?: number }) => {
        settled.retries.push(options ?? {});
      },
    },
  };
}

interface Call {
  url: string;
  init: RequestInit;
}

function origin(answers: (Response | Error)[]) {
  const calls: Call[] = [];
  const fetcher = {
    fetch: async (url: string, init: RequestInit) => {
      calls.push({ url, init });
      const answer = answers[Math.min(calls.length - 1, answers.length - 1)];
      if (answer instanceof Error) throw answer;
      return answer;
    },
  } as unknown as Fetcher;
  return { calls, env: { OCEL_ORIGIN_CLIENT_CERTIFICATE: fetcher } satisfies Env };
}

async function run(env: Env, ...items: ReturnType<typeof message>[]) {
  const batch = { queue: "refresh", messages: items.map((i) => i.message) };
  await worker.queue(batch as unknown as MessageBatch, env, {} as ExecutionContext);
}

const rendered = new Response(null, {
  status: 200,
  headers: { "x-ocel-rendered": "1700000000000" },
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("refresher", () => {
  it("acks a refresh once its origin answered 2xx", async () => {
    const { calls, env } = origin([rendered]);
    const item = message(body());

    await run(env, item);

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe(`${ORIGIN}/blog`);
    expect(calls[0].init.method).toBe("HEAD");
    expect(calls[0].init.redirect).toBe("manual");
    expect(new Headers(calls[0].init.headers).get("x-ocel-refresh")).toBe("1700000000000");
    expect(item.settled.acks).toBe(1);
    expect(item.settled.retries).toHaveLength(0);
  });

  it("acks a refresh whose origin answered without the expected header, as a route that went dynamic", async () => {
    const { env } = origin([new Response(null, { status: 200 })]);
    const log = vi.spyOn(console, "log").mockImplementation(() => {});
    const item = message(body());

    await run(env, item);

    expect(item.settled.acks).toBe(1);
    expect(item.settled.retries).toHaveLength(0);
    expect(JSON.parse(log.mock.calls[0][0]).event).toBe("RevalidateExpectMiss");
  });

  it("retries a refresh whose origin failed, waiting longer each attempt up to five minutes", async () => {
    const delays: (number | undefined)[] = [];
    for (const attempts of [1, 2, 4, 9]) {
      const { env } = origin([new Response(null, { status: 500 })]);
      const item = message(body(), attempts);
      await run(env, item);
      expect(item.settled.acks).toBe(0);
      delays.push(item.settled.retries[0]?.delaySeconds);
    }
    expect(delays).toEqual([15, 30, 120, 300]);
  });

  it("retries a refresh whose origin answered a redirect", async () => {
    const { env } = origin([new Response(null, { status: 302, headers: { location: "/x" } })]);
    const item = message(body());

    await run(env, item);

    expect(item.settled.acks).toBe(0);
    expect(item.settled.retries).toHaveLength(1);
  });

  it("retries a refresh whose fetch threw", async () => {
    const { env } = origin([new Error("connection reset")]);
    const item = message(body());

    await run(env, item);

    expect(item.settled.acks).toBe(0);
    expect(item.settled.retries).toEqual([{ delaySeconds: 15 }]);
  });

  it("drops a refresh whose header values cannot be sent instead of retrying it", async () => {
    const { calls, env } = origin([rendered]);
    const item = message(body({ headers: { "x-ocel-refresh": "a\r\nb" } }));

    await run(env, item);

    expect(calls).toHaveLength(0);
    expect(item.settled.acks).toBe(1);
    expect(item.settled.retries).toEqual([]);
  });

  it("drops a malformed refresh without fetching anything", async () => {
    const { calls, env } = origin([rendered]);
    const items = [
      message(42),
      message("{not json"),
      message(body({ v: 2 })),
      message(body({ routePath: "blog" })),
    ];

    await run(env, ...items);

    expect(calls).toHaveLength(0);
    for (const item of items) {
      expect(item.settled.acks).toBe(1);
      expect(item.settled.retries).toHaveLength(0);
    }
  });

  it("drops a refresh whose route path would leave its origin", async () => {
    const { calls, env } = origin([rendered]);
    const items = [
      message(body({ routePath: "//evil.example/x" })),
      message(body({ origin: "http://d1.origin.example" })),
      message(body({ origin: 7 })),
      message(body({ origin: "not a url" })),
    ];

    await run(env, ...items);

    expect(calls).toHaveLength(0);
    for (const item of items) {
      expect(item.settled.acks).toBe(1);
      expect(item.settled.retries).toHaveLength(0);
    }
  });

  it("reaches the origin only through the client certificate binding", async () => {
    const global = vi.spyOn(globalThis, "fetch");
    const { calls, env } = origin([rendered]);

    await run(env, message(body()));
    await run({}, message(body()));

    expect(calls).toHaveLength(1);
    expect(global).not.toHaveBeenCalled();
  });

  it("retries a refresh when no client certificate is bound", async () => {
    const item = message(body());

    await run({}, item);

    expect(item.settled.acks).toBe(0);
    expect(item.settled.retries).toEqual([{ delaySeconds: 15 }]);
  });

  it("never logs a refresh's headers or origin", async () => {
    const log = vi.spyOn(console, "log").mockImplementation(() => {});
    const failing = origin([
      new Response(null, { status: 500 }),
      new Error("d1.origin.example down"),
    ]);

    await run(failing.env, message(body()), message(body()));
    await run(origin([rendered]).env, message(body()));

    const lines = log.mock.calls.map((c) => String(c[0])).join("\n");
    expect(log).toHaveBeenCalledTimes(3);
    expect(lines).not.toContain("secret-header-value");
    expect(lines).not.toContain("origin.example");
  });

  it("settles every message of a batch even when one fails", async () => {
    const { env } = origin([new Response(null, { status: 500 }), rendered]);
    const first = message(body());
    const second = message(body());

    await run(env, first, second);

    expect(first.settled.retries).toHaveLength(1);
    expect(first.settled.acks).toBe(0);
    expect(second.settled.acks).toBe(1);
    expect(second.settled.retries).toHaveLength(0);
  });
});

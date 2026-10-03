import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { bindingKey } from "../binding/binding.js";
import { BindingType } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import {
  type FakeAppSync,
  type FakeAppSyncOptions,
  serveFakeAppSync,
} from "../testing/realtime-appsync.js";
import {
  type FakeGateway,
  type FakeGatewayOptions,
  serveFakeGateway,
} from "../testing/realtime-gateway.js";

vi.mock("../runtime/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { realtime } = await import("./index.js");
const { createWebRealtimeHandler } = await import("./handler.js");
const { createRealtimeClient, RealtimeError } = await import("./client.js");

const OrderEvent = z.object({ status: z.string() });
const ChatMessage = z.object({ text: z.string().max(20) });

const owners = new Map([["o-1", "u1"]]);
let flakyRuleFailures = 0;

const rt = realtime("app", {
  authorize: async (request) => {
    const user = request.headers.get("x-user");
    return user ? { id: user } : null;
  },
  channels: {
    "orders/:orderId": {
      schema: OrderEvent,
      subscribe: ({ auth, params }) => owners.get(params.orderId) === auth.id,
    },
    "projects/:projectId/deploys/:deployId": {
      schema: OrderEvent,
      wildcard: true,
      subscribe: () => true,
    },
    "rooms/:roomId": {
      schema: ChatMessage,
      subscribe: () => true,
      publish: ({ auth }) => auth.id === "u1",
    },
    status: { schema: OrderEvent, subscribe: "public" },
    "flaky/:flakyId": {
      schema: OrderEvent,
      subscribe: () => {
        if (flakyRuleFailures === 0) return true;
        flakyRuleFailures -= 1;
        throw new Error("the rule's store is down");
      },
    },
  },
});

interface BatchBody {
  connect?: boolean;
  ops: { op: string; pattern: string }[];
}

interface App {
  url: string;
  requests: BatchBody[];
  close(): Promise<void>;
}

type AppAnswer = (body: BatchBody) => Response | "silent" | undefined;

async function serveApp(answer: AppAnswer = () => undefined): Promise<App> {
  const { POST } = createWebRealtimeHandler(rt);
  const requests: App["requests"] = [];
  const server: Server = createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(chunk as Buffer);
    const body = Buffer.concat(chunks).toString("utf8");
    const parsed: BatchBody = JSON.parse(body);
    requests.push(parsed);
    const override = answer(parsed);
    if (override === "silent") return;
    const headers = new Headers();
    for (const [name, value] of Object.entries(req.headers)) {
      if (typeof value === "string") headers.set(name, value);
    }
    const response =
      override ??
      (await POST(
        new Request(`http://${req.headers.host}${req.url}`, { method: "POST", headers, body }),
      ));
    res.writeHead(response.status, Object.fromEntries(response.headers));
    res.end(await response.text());
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as AddressInfo;
  return {
    url: `http://127.0.0.1:${port}/api/realtime`,
    requests,
    close: () =>
      new Promise<void>((resolve) => {
        server.closeAllConnections();
        server.close(() => resolve());
      }),
  };
}

function waitFor(condition: () => boolean, what: string): Promise<void> {
  return vi.waitFor(
    () => {
      if (!condition()) throw new Error(`still waiting for ${what}`);
    },
    { timeout: 4_000, interval: 5 },
  );
}

async function until(condition: () => boolean): Promise<void> {
  while (!condition()) await new Promise((resolve) => setImmediate(resolve));
}

let gateway: FakeGateway;
let app: App;

async function replaceGateway(options: FakeGatewayOptions): Promise<void> {
  await gateway.close();
  gateway = await serveFakeGateway(options);
  vi.stubEnv(bindingKey("app", BindingType.REALTIME), gateway.binding);
}

async function replaceApp(answer: AppAnswer): Promise<void> {
  await app.close();
  app = await serveApp(answer);
}

beforeEach(async () => {
  gateway = await serveFakeGateway();
  vi.stubEnv("OCEL_PHASE", "");
  vi.stubEnv(bindingKey("app", BindingType.REALTIME), gateway.binding);
  app = await serveApp();
});

afterEach(async () => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  flakyRuleFailures = 0;
  await app.close();
  await gateway.close();
});

describe("a realtime client subscribing", () => {
  it("receives each event the server publishes on a public channel, with its envelope's meta", async () => {
    const live = createRealtimeClient<typeof rt>({ url: app.url });
    const received: { event: unknown; meta: unknown }[] = [];
    live.subscribe("status", {}, (event, meta) => received.push({ event, meta }));
    await waitFor(() => gateway.subscribes.length === 1, "the subscribe");

    await rt.publish("status", { body: { status: "up" } });

    await waitFor(() => received.length === 1, "the event");
    expect(received[0]).toEqual({
      event: { status: "up" },
      meta: {
        id: expect.stringMatching(/^[0-9a-f]{32}$/),
        channel: "/app/status",
        publishedAt: expect.any(Number),
      },
    });
    live.close();
  });

  it("authorizes the subscriptions made together in one request, at most 50 ops each", async () => {
    const live = createRealtimeClient<typeof rt>({ url: app.url, headers: { "x-user": "u1" } });
    for (let i = 0; i < 51; i++) {
      live.subscribe(
        "projects/:projectId/deploys/:deployId",
        { params: { projectId: `p${i}` } },
        () => {},
      );
    }
    await waitFor(() => gateway.subscribes.length === 51, "every subscribe");

    expect(app.requests.map((request) => [request.connect, request.ops.length])).toEqual([
      [true, 50],
      [undefined, 1],
    ]);
    expect(gateway.sockets).toHaveLength(1);
    live.close();
  });

  it("receives the events of every channel under a wildcard subscription", async () => {
    const live = createRealtimeClient<typeof rt>({ url: app.url, headers: { "x-user": "u1" } });
    const channels: string[] = [];
    live.subscribe(
      "projects/:projectId/deploys/:deployId",
      { params: { projectId: "p-1" } },
      (_, meta) => channels.push(meta.channel),
    );
    await waitFor(() => gateway.subscribes.length === 1, "the subscribe");

    await rt.publish("projects/:projectId/deploys/:deployId", {
      params: { projectId: "p-1", deployId: "d-1" },
      body: { status: "live" },
    });
    await rt.publish("projects/:projectId/deploys/:deployId", {
      params: { projectId: "p-2", deployId: "d-1" },
      body: { status: "live" },
    });
    await rt.publish("projects/:projectId/deploys/:deployId", {
      params: { projectId: "p-1", deployId: "d-2" },
      body: { status: "live" },
    });

    await waitFor(() => channels.length === 2, "both events");
    expect(channels).toEqual(["/app/projects/p-1/deploys/d-1", "/app/projects/p-1/deploys/d-2"]);
    live.close();
  });

  it("reports a denied subscription as a non-retriable error naming the denial", async () => {
    const live = createRealtimeClient<typeof rt>({ url: app.url, headers: { "x-user": "u2" } });
    const errors: InstanceType<typeof RealtimeError>[] = [];
    live.subscribe(
      "orders/:orderId",
      { params: { orderId: "o-1" }, onError: (error) => errors.push(error) },
      () => {},
    );

    await waitFor(() => errors.length === 1, "the denial");
    expect(errors[0]).toBeInstanceOf(RealtimeError);
    expect(errors[0]).toMatchObject({ code: "forbidden", retriable: false });
    expect(gateway.subscribes).toEqual([]);
    live.close();
  });

  it("stops delivering a channel's events once unsubscribed", async () => {
    const live = createRealtimeClient<typeof rt>({ url: app.url });
    const received: unknown[] = [];
    const stop = live.subscribe("status", {}, (event) => received.push(event));
    const kept: unknown[] = [];
    live.subscribe("status", {}, (event) => kept.push(event));
    await waitFor(() => gateway.subscribes.length === 2, "both subscribes");

    stop();
    await rt.publish("status", { body: { status: "up" } });

    await waitFor(() => kept.length === 1, "the kept subscription's event");
    expect(received).toEqual([]);
    live.close();
  });
});

describe("a realtime client's connection", () => {
  it("is idle until a subscription needs a socket, connecting until it opens, then connected", async () => {
    const live = createRealtimeClient<typeof rt>({ url: app.url });
    const states: string[] = [live.state];
    live.onStateChange((state) => states.push(state));

    live.subscribe("status", {}, () => {});

    await waitFor(() => live.state === "connected", "the connection");
    expect(states).toEqual(["idle", "connecting", "connected"]);
    live.close();
  });

  it("reconnects after a drop and authorizes every live subscription again in one request", async () => {
    const live = createRealtimeClient<typeof rt>({
      url: app.url,
      headers: () => ({ "x-user": "u1" }),
    });
    const states: string[] = [];
    live.onStateChange((state) => states.push(state));
    const received: unknown[] = [];
    live.subscribe("status", {}, (event) => received.push(event));
    live.subscribe("orders/:orderId", { params: { orderId: "o-1" } }, (event) =>
      received.push(event),
    );
    await waitFor(() => gateway.subscribes.length === 2, "both subscribes");

    gateway.dropSockets();

    await waitFor(() => gateway.subscribes.length === 4, "both subscriptions again");
    await waitFor(() => live.state === "connected", "the reconnect");
    expect(states).toEqual(["connecting", "connected", "reconnecting", "connected"]);
    expect(gateway.sockets).toHaveLength(2);
    expect(app.requests.at(-1)).toEqual({
      connect: true,
      ops: [
        { op: "subscribe", pattern: "status", params: {} },
        { op: "subscribe", pattern: "orders/:orderId", params: { orderId: "o-1" } },
      ],
    });
    await rt.publish("orders/:orderId", { params: { orderId: "o-1" }, body: { status: "paid" } });
    await waitFor(() => received.length === 1, "the event after reconnecting");
    live.close();
  });

  it("loses a channel on reconnect when its rule no longer admits the caller", async () => {
    owners.set("o-2", "u1");
    const live = createRealtimeClient<typeof rt>({ url: app.url, headers: { "x-user": "u1" } });
    const errors: InstanceType<typeof RealtimeError>[] = [];
    live.subscribe(
      "orders/:orderId",
      { params: { orderId: "o-2" }, onError: (error) => errors.push(error) },
      () => {},
    );
    await waitFor(() => gateway.subscribes.length === 1, "the subscribe");

    owners.set("o-2", "u9");
    gateway.dropSockets();

    await waitFor(() => errors.some((error) => !error.retriable), "the revocation");
    expect(errors.map((error) => [error.code, error.retriable])).toEqual([
      ["connection-lost", true],
      ["forbidden", false],
    ]);
    expect(gateway.subscribes).toHaveLength(1);
    live.close();
  });
});

describe("a realtime client publishing", () => {
  it("relays a publish through the handler's publish rule to every subscriber", async () => {
    const live = createRealtimeClient<typeof rt>({ url: app.url, headers: { "x-user": "u1" } });
    const received: unknown[] = [];
    live.subscribe("rooms/:roomId", { params: { roomId: "r-1" } }, (event) => received.push(event));
    await waitFor(() => gateway.subscribes.length === 1, "the subscribe");

    await live.publish("rooms/:roomId", { params: { roomId: "r-1" }, body: { text: "hi" } });

    await waitFor(() => received.length === 1, "the event");
    expect(received).toEqual([{ text: "hi" }]);
    live.close();
  });

  it("rejects a publish the rule denies with a non-retriable error, and opens no socket", async () => {
    const live = createRealtimeClient<typeof rt>({ url: app.url, headers: { "x-user": "u2" } });

    const refused = live.publish("rooms/:roomId", {
      params: { roomId: "r-1" },
      body: { text: "hi" },
    });

    await expect(refused).rejects.toMatchObject({ code: "forbidden", retriable: false });
    expect(app.requests).toEqual([
      {
        ops: [
          {
            op: "publish",
            pattern: "rooms/:roomId",
            params: { roomId: "r-1" },
            body: { text: "hi" },
          },
        ],
      },
    ]);
    expect(gateway.sockets).toEqual([]);
    expect(gateway.published).toEqual([]);
    live.close();
  });

  it("rejects with a retriable error when the handler cannot be reached", async () => {
    const live = createRealtimeClient<typeof rt>({ url: "http://127.0.0.1:1/api/realtime" });

    const refused = live.publish("rooms/:roomId", {
      params: { roomId: "r-1" },
      body: { text: "hi" },
    });

    await expect(refused).rejects.toMatchObject({ code: "handler-unreachable", retriable: true });
    live.close();
  });

  it("rejects with a retriable error when the handler does not answer within 10 seconds", async () => {
    await replaceApp(() => "silent");
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const live = createRealtimeClient<typeof rt>({ url: app.url });
    let isSettled = false;
    const refused = live
      .publish("rooms/:roomId", { params: { roomId: "r-1" }, body: { text: "hi" } })
      .finally(() => {
        isSettled = true;
      });
    refused.catch(() => {});
    await until(() => app.requests.length === 1);

    await vi.advanceTimersByTimeAsync(9_999);
    expect(isSettled).toBe(false);
    await vi.advanceTimersByTimeAsync(1);

    await expect(refused).rejects.toMatchObject({ code: "handler-unreachable", retriable: true });
    live.close();
  });

  it("rejects with a retriable error when its headers cannot be read, sending nothing", async () => {
    const live = createRealtimeClient<typeof rt>({
      url: app.url,
      headers: () => Promise.reject(new Error("session expired")),
    });

    const refused = live.publish("rooms/:roomId", {
      params: { roomId: "r-1" },
      body: { text: "hi" },
    });

    await expect(refused).rejects.toMatchObject({ code: "headers-failed", retriable: true });
    expect(app.requests).toEqual([]);
    live.close();
  });
});

describe("a realtime client recovering a subscription", () => {
  it("keeps trying a subscription whose rule failed, and delivers once the rule admits it", async () => {
    flakyRuleFailures = 1;
    const live = createRealtimeClient<typeof rt>({ url: app.url, headers: { "x-user": "u1" } });
    const errors: InstanceType<typeof RealtimeError>[] = [];
    const received: unknown[] = [];
    live.subscribe(
      "flaky/:flakyId",
      { params: { flakyId: "f-1" }, onError: (error) => errors.push(error) },
      (event) => received.push(event),
    );
    await waitFor(() => gateway.subscribes.length === 1, "the retried subscribe");

    await rt.publish("flaky/:flakyId", { params: { flakyId: "f-1" }, body: { status: "up" } });

    await waitFor(() => received.length === 1, "the event");
    expect(errors.map((error) => [error.code, error.retriable])).toEqual([["rule-error", true]]);
    live.close();
  });

  it("reports a subscribe the handler granted without a token as a non-retriable error", async () => {
    await replaceApp(() =>
      Response.json({
        transport: "ocel-gateway",
        url: gateway.url,
        grants: [{ i: 0, wire: "/app/status" }],
        denied: [],
      }),
    );
    const live = createRealtimeClient<typeof rt>({ url: app.url });
    const errors: InstanceType<typeof RealtimeError>[] = [];
    live.subscribe("status", { onError: (error) => errors.push(error) }, () => {});

    await waitFor(() => errors.length === 1, "the error");
    expect(errors[0]).toMatchObject({ code: "invalid-grant", retriable: false });
    expect(gateway.sockets).toEqual([]);
    live.close();
  });

  it("reports a drop once to a subscription still waiting on the gateway", async () => {
    await replaceGateway({ answeringSubscribes: false });
    const live = createRealtimeClient<typeof rt>({ url: app.url });
    const errors: InstanceType<typeof RealtimeError>[] = [];
    live.subscribe("status", { onError: (error) => errors.push(error) }, () => {});
    await waitFor(() => gateway.subscribes.length === 1, "the subscribe");

    gateway.dropSockets();

    await waitFor(() => gateway.subscribes.length === 2, "the subscribe after reconnecting");
    expect(errors.map((error) => error.code)).toEqual(["connection-lost"]);
    live.close();
  });

  it("gives up on a socket the gateway does not accept within 10 seconds, and tries again", async () => {
    await replaceGateway({ acknowledgingConnections: false });
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const live = createRealtimeClient<typeof rt>({ url: app.url });
    const errors: InstanceType<typeof RealtimeError>[] = [];
    live.subscribe("status", { onError: (error) => errors.push(error) }, () => {});
    await until(() => gateway.sockets.length === 1);

    await vi.advanceTimersByTimeAsync(9_999);
    expect(errors).toEqual([]);
    await vi.advanceTimersByTimeAsync(1);

    await until(() => errors.length === 1);
    expect(errors[0]).toMatchObject({ code: "connection-failed", retriable: true });
    await vi.advanceTimersByTimeAsync(500);
    await until(() => gateway.sockets.length === 2);
    live.close();
  });

  it("drops a socket on which a subscribe goes unanswered for 10 seconds, and reconnects", async () => {
    await replaceGateway({ answeringSubscribes: false });
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const live = createRealtimeClient<typeof rt>({ url: app.url });
    const errors: InstanceType<typeof RealtimeError>[] = [];
    live.subscribe("status", { onError: (error) => errors.push(error) }, () => {});
    await until(() => gateway.subscribes.length === 1);

    await vi.advanceTimersByTimeAsync(9_999);
    expect(errors).toEqual([]);
    await vi.advanceTimersByTimeAsync(1);

    await until(() => errors.length === 1);
    expect(errors[0]).toMatchObject({ code: "connection-lost", retriable: true });
    await vi.advanceTimersByTimeAsync(500);
    await until(() => gateway.sockets.length === 2);
    live.close();
  });

  it("waits a jittered delay that doubles up to 30 seconds, and starts over once connected", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    vi.spyOn(Math, "random").mockReturnValue(0.5);
    const realFetch = globalThis.fetch;
    let isRefusing = true;
    vi.stubGlobal("fetch", (input: string | URL | Request, init?: RequestInit) =>
      isRefusing ? Promise.resolve(new Response(null, { status: 503 })) : realFetch(input, init),
    );
    const attempts: number[] = [];
    const live = createRealtimeClient<typeof rt>({
      url: app.url,
      headers: () => {
        attempts.push(Date.now());
        return {};
      },
    });
    live.subscribe("status", {}, () => {});

    await vi.advanceTimersByTimeAsync(46_000);
    expect(attempts.slice(1).map((at, i) => at - (attempts[i] ?? 0))).toEqual([
      250, 500, 1_000, 2_000, 4_000, 8_000, 15_000, 15_000,
    ]);

    isRefusing = false;
    await vi.advanceTimersByTimeAsync(15_000);
    await until(() => live.state === "connected");
    isRefusing = true;
    gateway.dropSockets();
    await until(() => live.state === "reconnecting");
    const droppedAt = Date.now();
    const attemptsBeforeRetry = attempts.length;

    await vi.advanceTimersByTimeAsync(249);
    expect(attempts).toHaveLength(attemptsBeforeRetry);
    await vi.advanceTimersByTimeAsync(1);
    expect(attempts.at(-1)).toBe(droppedAt + 250);
    live.close();
  });
});

describe("a realtime client's state", () => {
  it("stays idle while it only publishes, and is closed once closed", async () => {
    const live = createRealtimeClient<typeof rt>({ url: app.url, headers: { "x-user": "u1" } });

    await live.publish("rooms/:roomId", { params: { roomId: "r-1" }, body: { text: "hi" } });
    expect(live.state).toBe("idle");

    live.close();
    expect(live.state).toBe("closed");
  });

  it("is idle after a drop that leaves no subscription to restore", async () => {
    const live = createRealtimeClient<typeof rt>({ url: app.url });
    const states: string[] = [];
    live.onStateChange((state) => states.push(state));
    const stop = live.subscribe("status", {}, () => {});
    await waitFor(() => live.state === "connected", "the connection");

    stop();
    gateway.dropSockets();

    await waitFor(() => live.state === "idle", "idle");
    expect(states).toEqual(["connecting", "connected", "idle"]);
    live.close();
  });
});

describe("a realtime client on AppSync Events", () => {
  let appsync: FakeAppSync;

  async function serveAppSync(options: FakeAppSyncOptions = {}): Promise<void> {
    await appsync?.close();
    appsync = await serveFakeAppSync(options);
    vi.stubEnv(bindingKey("app", BindingType.REALTIME), appsync.binding);
  }

  function envelopeOn(channel: string, data: unknown): string {
    return JSON.stringify({
      v: 1,
      id: "0123456789abcdef0123456789abcdef",
      ch: channel,
      ts: 1_790_000_000_000,
      kind: "live",
      data,
    });
  }

  beforeEach(async () => {
    await serveAppSync();
  });

  afterEach(async () => {
    await appsync.close();
  });

  it("connects with the API's host in its header and authorizes each subscribe with its own token", async () => {
    const live = createRealtimeClient<typeof rt>({ url: app.url, headers: { "x-user": "u1" } });
    live.subscribe("orders/:orderId", { params: { orderId: "o-1" } }, () => {});
    await waitFor(() => appsync.subscribes.length === 1, "the subscribe");

    expect(appsync.connects).toEqual([{ host: appsync.host, Authorization: expect.any(String) }]);
    expect(appsync.subscribes[0]).toEqual({
      id: expect.any(String),
      channel: "/app/orders/o-1",
      authorization: { host: appsync.host, Authorization: expect.any(String) },
    });
    expect(live.state).toBe("connected");
    live.close();
  });

  it.each(["string", "array", "double-encoded"] as const)(
    "receives an event AppSync carries as %s",
    async (eventForm) => {
      await serveAppSync({ eventForm });
      const live = createRealtimeClient<typeof rt>({ url: app.url });
      const received: { event: unknown; meta: unknown }[] = [];
      live.subscribe("status", {}, (event, meta) => received.push({ event, meta }));
      await waitFor(() => appsync.subscribes.length === 1, "the subscribe");

      appsync.broadcast("/app/status", envelopeOn("/app/status", { status: "up" }));

      await waitFor(() => received.length === 1, "the event");
      expect(received[0]).toEqual({
        event: { status: "up" },
        meta: {
          id: "0123456789abcdef0123456789abcdef",
          channel: "/app/status",
          publishedAt: 1_790_000_000_000,
        },
      });
      live.close();
    },
  );

  it("reports a connect AppSync refuses as a retriable failure naming why", async () => {
    await serveAppSync({ clockOffsetSeconds: 600 });
    const live = createRealtimeClient<typeof rt>({ url: app.url });
    const errors: InstanceType<typeof RealtimeError>[] = [];
    live.subscribe("status", { onError: (error) => errors.push(error) }, () => {});

    await waitFor(() => errors.length === 1, "the refusal");
    expect(errors[0]).toMatchObject({ code: "connection-failed", retriable: true });
    expect(errors[0]?.message).toContain("the token has expired");
    live.close();
  });

  it("reports an expired subscribe token AppSync refuses as a non-retriable subscribe-refused", async () => {
    const live = createRealtimeClient<typeof rt>({ url: app.url, headers: { "x-user": "u1" } });
    const errors: InstanceType<typeof RealtimeError>[] = [];
    live.subscribe("status", {}, () => {});
    await waitFor(() => appsync.subscribes.length === 1, "the first subscribe");
    appsync.options.clockOffsetSeconds = 600;

    live.subscribe(
      "orders/:orderId",
      { params: { orderId: "o-1" }, onError: (error) => errors.push(error) },
      () => {},
    );

    await waitFor(() => errors.length === 1, "the refusal");
    expect(errors[0]).toMatchObject({ code: "subscribe-refused", retriable: false });
    expect(errors[0]?.message).toContain("AppSync refused the subscription");
    expect(live.state).toBe("connected");
    live.close();
  });

  it("keeps a subscription whose event AppSync failed to broadcast, and delivers the next", async () => {
    const live = createRealtimeClient<typeof rt>({ url: app.url });
    const received: unknown[] = [];
    const errors: unknown[] = [];
    live.subscribe("status", { onError: (error) => errors.push(error) }, (event) =>
      received.push(event),
    );
    await waitFor(() => appsync.subscribes.length === 1, "the subscribe");

    appsync.breakBroadcast("/app/status");
    appsync.broadcast("/app/status", envelopeOn("/app/status", { status: "after" }));

    await waitFor(() => received.length === 1, "the next event");
    expect(received).toEqual([{ status: "after" }]);
    expect(errors).toEqual([]);
    expect(live.state).toBe("connected");
    live.close();
  });

  it("drops a socket whose keep-alives stop for connectionTimeoutMs, and reconnects", async () => {
    await serveAppSync({ keepAliveMilliseconds: 20, connectionTimeoutMilliseconds: 200 });
    const live = createRealtimeClient<typeof rt>({ url: app.url });
    const errors: InstanceType<typeof RealtimeError>[] = [];
    live.subscribe("status", { onError: (error) => errors.push(error) }, () => {});
    await waitFor(() => appsync.subscribes.length === 1, "the subscribe");

    appsync.silence();

    await waitFor(() => errors.length === 1, "the drop");
    expect(errors[0]).toMatchObject({ code: "connection-lost", retriable: true });
    expect(errors[0]?.message).toContain("AppSync sent nothing for 200ms");
    await waitFor(() => appsync.connects.length === 2, "the reconnect");
  });

  it("asks the handler for every live subscription again when AppSync ends a connection at its lifetime", async () => {
    await serveAppSync({ connectionLifetimeMilliseconds: 300 });
    const live = createRealtimeClient<typeof rt>({ url: app.url, headers: { "x-user": "u1" } });
    const received: unknown[] = [];
    live.subscribe("status", {}, (event) => received.push(event));
    live.subscribe("orders/:orderId", { params: { orderId: "o-1" } }, (event) =>
      received.push(event),
    );
    await waitFor(() => appsync.subscribes.length === 2, "both subscribes");
    const requestsBefore = app.requests.length;

    await waitFor(() => appsync.subscribes.length === 4, "both subscriptions again");

    expect(app.requests.slice(requestsBefore)).toContainEqual({
      connect: true,
      ops: [
        { op: "subscribe", pattern: "status", params: {} },
        { op: "subscribe", pattern: "orders/:orderId", params: { orderId: "o-1" } },
      ],
    });
    await waitFor(() => live.state === "connected", "the new socket");
    appsync.broadcast("/app/orders/o-1", envelopeOn("/app/orders/o-1", { status: "paid" }));
    await waitFor(() => received.length >= 1, "an event on the new socket");
    live.close();
  });
});

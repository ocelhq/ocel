import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { bindingKey } from "../binding/binding.js";
import { BindingType } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { type FakeGateway, serveFakeGateway } from "../testing/realtime-gateway.js";

vi.mock("../runtime/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { realtime } = await import("./index.js");
const { createWebRealtimeHandler } = await import("./handler.js");
const { createRealtimeClient, RealtimeError } = await import("./client.js");

const OrderEvent = z.object({ status: z.string() });
const ChatMessage = z.object({ text: z.string().max(20) });

const owners = new Map([["o-1", "u1"]]);

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
  },
});

interface App {
  url: string;
  requests: { connect?: boolean; ops: { op: string; pattern: string }[] }[];
  close(): Promise<void>;
}

async function serveApp(): Promise<App> {
  const { POST } = createWebRealtimeHandler(rt);
  const requests: App["requests"] = [];
  const server: Server = createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(chunk as Buffer);
    const body = Buffer.concat(chunks).toString("utf8");
    requests.push(JSON.parse(body));
    const headers = new Headers();
    for (const [name, value] of Object.entries(req.headers)) {
      if (typeof value === "string") headers.set(name, value);
    }
    const response = await POST(
      new Request(`http://${req.headers.host}${req.url}`, { method: "POST", headers, body }),
    );
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

let gateway: FakeGateway;
let app: App;

beforeEach(async () => {
  gateway = await serveFakeGateway();
  vi.stubEnv("OCEL_PHASE", "");
  vi.stubEnv(bindingKey("app", BindingType.REALTIME), gateway.binding);
  app = await serveApp();
});

afterEach(async () => {
  vi.unstubAllEnvs();
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
        ts: expect.any(Number),
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

    expect(app.requests.map((r) => [r.connect, r.ops.length])).toEqual([
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
      { params: { orderId: "o-1" }, onError: (e) => errors.push(e) },
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
  it("is connecting until its socket opens, then connected", async () => {
    const live = createRealtimeClient<typeof rt>({ url: app.url });
    const states: string[] = [live.state];
    live.onStateChange((state) => states.push(state));

    live.subscribe("status", {}, () => {});

    await waitFor(() => live.state === "connected", "the connection");
    expect(states).toEqual(["connecting", "connected"]);
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
    expect(states).toEqual(["connected", "reconnecting", "connected"]);
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
      { params: { orderId: "o-2" }, onError: (e) => errors.push(e) },
      () => {},
    );
    await waitFor(() => gateway.subscribes.length === 1, "the subscribe");

    owners.set("o-2", "u9");
    gateway.dropSockets();

    await waitFor(() => errors.some((e) => !e.retriable), "the revocation");
    expect(errors.map((e) => [e.code, e.retriable])).toEqual([
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

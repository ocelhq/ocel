import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { bindingKey } from "../binding/binding.js";
import {
  RealtimePublish,
  RealtimeSubscribe,
  ResourceType,
} from "../gen/proto/app/resources/v1/resources_pb.js";
import { BindingType } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import {
  appsyncBinding,
  type FakeGateway,
  readClaims,
  serveFakeGateway,
} from "../testing/realtime-gateway.js";

const declareMock = vi.hoisted(() => vi.fn((_req: unknown) => Promise.resolve({})));

vi.mock("../runtime/rpc", () => ({
  rpc: { resource: { declare: declareMock } },
}));

const { realtime, RealtimePublishError, UnprovisionedResourceError } = await import("./index.js");

describe("realtime discovery declare", () => {
  beforeEach(() => {
    declareMock.mockClear();
  });

  it("declares a REALTIME with each channel's pattern, wildcard, schema and access, and the default token ttl", () => {
    realtime("app", {
      authorize: async () => ({ id: "u1" }),
      channels: {
        "orders/:orderId": {
          schema: z.object({ status: z.string() }),
          subscribe: async () => true,
        },
        "projects/:projectId/deploys/:deployId": {
          schema: z.object({ step: z.string() }),
          wildcard: true,
          subscribe: async () => true,
        },
        "rooms/:roomId": {
          schema: z.object({ text: z.string() }),
          subscribe: async () => true,
          publish: async () => true,
        },
        status: { schema: z.object({ up: z.boolean() }), subscribe: "public" },
      },
    });

    const [request] = declareMock.mock.calls[0] as [
      {
        resource: unknown;
        config: {
          case: string;
          value: {
            tokenTtl: unknown;
            channels: {
              pattern: string;
              schema: string;
              wildcard: boolean;
              subscribe: number;
              publish: number;
              source: string;
            }[];
          };
        };
        source: string;
      },
    ];
    expect(request.resource).toEqual({ name: "app", type: ResourceType.REALTIME });
    expect(request.config.case).toBe("realtime");
    expect(request.config.value.tokenTtl).toEqual({ seconds: 60n, nanos: 0 });
    expect(
      request.config.value.channels.map(({ pattern, wildcard, subscribe, publish }) => ({
        pattern,
        wildcard,
        subscribe,
        publish,
      })),
    ).toEqual([
      {
        pattern: "orders/:orderId",
        wildcard: false,
        subscribe: RealtimeSubscribe.RULE,
        publish: RealtimePublish.SERVER,
      },
      {
        pattern: "projects/:projectId/deploys/:deployId",
        wildcard: true,
        subscribe: RealtimeSubscribe.RULE,
        publish: RealtimePublish.SERVER,
      },
      {
        pattern: "rooms/:roomId",
        wildcard: false,
        subscribe: RealtimeSubscribe.RULE,
        publish: RealtimePublish.RULE,
      },
      {
        pattern: "status",
        wildcard: false,
        subscribe: RealtimeSubscribe.PUBLIC,
        publish: RealtimePublish.SERVER,
      },
    ]);
    expect(JSON.parse(request.config.value.channels[3]?.schema ?? "")).toMatchObject({
      type: "object",
      properties: { up: { type: "boolean" } },
    });
    expect(request.config.value.channels[0]?.source).toBe(request.source);
  });

  it("declares the token ttl it was given", () => {
    realtime("short", { tokenTtl: "30s", channels: {} });

    const [request] = declareMock.mock.calls[0] as [{ config: { value: { tokenTtl: unknown } } }];
    expect(request.config.value.tokenTtl).toEqual({ seconds: 30n, nanos: 0 });
  });

  it.each([
    ["5s", "outside 10s to 300s"],
    ["301s", "outside 10s to 300s"],
  ] as const)("refuses a token ttl of %s", (tokenTtl, reason) => {
    expect(() => realtime("bad-ttl", { tokenTtl, channels: {} })).toThrow(reason);
  });

  it("refuses a name that cannot begin a channel", () => {
    expect(() => realtime("my_app", { channels: {} })).toThrow('realtime("my_app")');
  });

  it("refuses a pattern its grammar does not allow, naming the resource", () => {
    expect(() =>
      realtime("app", {
        channels: { "orders/:o/:o": { schema: z.object({}), subscribe: "public" } },
      }),
    ).toThrow(/realtime\("app"\).*twice/);
  });

  it.each([
    ["a subscribe rule", { schema: z.object({}), subscribe: () => true }],
    ["a publish rule", { schema: z.object({}), subscribe: "public", publish: () => true }],
  ] as const)(
    "refuses a channel with %s when no authorize is declared, naming the resource and pattern",
    (_, channel) => {
      expect(() => realtime("app", { channels: { "rooms/:roomId": channel } })).toThrow(
        /realtime\("app"\).*"rooms\/:roomId".*authorize/,
      );
    },
  );

  it("declares public channels with no authorize", () => {
    expect(() =>
      realtime("app", { channels: { status: { schema: z.object({}), subscribe: "public" } } }),
    ).not.toThrow();
  });
});

const rt = realtime("app", {
  channels: {
    "orders/:orderId": {
      schema: z.object({ status: z.enum(["paid", "shipped"]) }),
      subscribe: "public",
    },
  },
});

describe("publishing from the server", () => {
  let gateway: FakeGateway;

  beforeEach(async () => {
    gateway = await serveFakeGateway();
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv(bindingKey("app", BindingType.REALTIME), gateway.binding);
  });

  afterEach(async () => {
    vi.unstubAllEnvs();
    await gateway.close();
  });

  it("posts the event's envelope to the gateway with a publish token for its wire channel", async () => {
    await rt.publish("orders/:orderId", {
      params: { orderId: "o_1" },
      body: { status: "shipped" },
    });

    expect(gateway.published).toHaveLength(1);
    const [event] = gateway.published;
    expect(event?.path).toBe("/publish");
    expect(event?.envelope).toEqual({
      v: 1,
      id: expect.stringMatching(/^[0-9a-f]{32}$/),
      ch: "/app/orders/0zn5ptc",
      ts: expect.any(Number),
      kind: "live",
      data: { status: "shipped" },
    });
    const claims = readClaims(event?.authorization?.replace(/^Bearer /, "") ?? "");
    expect(claims).toMatchObject({
      iss: "ocel:rt:app",
      aud: gateway.host,
      sub: "server",
      ocel: { op: "publish", ch: "/app/orders/0zn5ptc", ns: "app" },
    });
  });

  it("refuses a body its schema rejects, publishing nothing", async () => {
    const publishing = rt.publish("orders/:orderId", {
      params: { orderId: "o1" },
      body: { status: "lost" as "paid" },
    });

    await expect(publishing).rejects.toBeInstanceOf(RealtimePublishError);
    await expect(publishing).rejects.toMatchObject({ code: "invalid-body" });
    expect(gateway.published).toEqual([]);
  });

  it("refuses params that leave one off, publishing nothing", async () => {
    const publishing = rt.publish("orders/:orderId", {
      params: {} as { orderId: string },
      body: { status: "paid" },
    });

    await expect(publishing).rejects.toMatchObject({ code: "missing-param" });
    expect(gateway.published).toEqual([]);
  });

  it("refuses an event over 240 KiB", async () => {
    const big = realtime("app", {
      channels: { blobs: { schema: z.object({ data: z.string() }), subscribe: "public" } },
    });

    await expect(
      big.publish("blobs", { body: { data: "x".repeat(240 * 1024) } }),
    ).rejects.toMatchObject({
      code: "body-too-large",
    });
  });

  it("throws when the gateway refuses the publish", async () => {
    await gateway.close();
    gateway = await serveFakeGateway({ status: 401 });
    vi.stubEnv(bindingKey("app", BindingType.REALTIME), gateway.binding);

    await expect(
      rt.publish("orders/:orderId", { params: { orderId: "o1" }, body: { status: "paid" } }),
    ).rejects.toThrow("status 401");
  });

  it("throws when the gateway has not answered within 10 seconds", async () => {
    await gateway.close();
    gateway = await serveFakeGateway({ answering: false });
    vi.stubEnv(bindingKey("app", BindingType.REALTIME), gateway.binding);
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });

    try {
      let settled = false;
      const publishing = rt
        .publish("orders/:orderId", { params: { orderId: "o1" }, body: { status: "paid" } })
        .finally(() => {
          settled = true;
        });
      publishing.catch(() => {});
      while (gateway.published.length === 0) await new Promise((resolve) => setImmediate(resolve));
      await vi.advanceTimersByTimeAsync(9_999);
      expect(settled).toBe(false);
      await vi.advanceTimersByTimeAsync(1);

      await expect(publishing).rejects.toThrow();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("publishing outside a provisioned run", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("throws UnprovisionedResourceError during discovery", async () => {
    await expect(
      rt.publish("orders/:orderId", { params: { orderId: "o1" }, body: { status: "paid" } }),
    ).rejects.toBeInstanceOf(UnprovisionedResourceError);
  });

  it("names the binding key when nothing was delivered", async () => {
    vi.stubEnv("OCEL_PHASE", "");

    await expect(
      rt.publish("orders/:orderId", { params: { orderId: "o1" }, body: { status: "paid" } }),
    ).rejects.toThrow("OCEL_RESOURCE_REALTIME_app");
  });

  it("says publishing on AppSync Events is not supported yet", async () => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv(bindingKey("app", BindingType.REALTIME), appsyncBinding);

    await expect(
      rt.publish("orders/:orderId", { params: { orderId: "o1" }, body: { status: "paid" } }),
    ).rejects.toThrow("AppSync Events is not supported yet");
  });
});

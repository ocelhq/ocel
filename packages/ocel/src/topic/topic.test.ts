import { type Timestamp, timestampDate } from "@bufbuild/protobuf/wkt";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { ResourceType } from "../gen/proto/app/resources/v1/resources_pb.js";
import { Lane, type SendRequest, TopicService } from "../gen/proto/app/topic/v1/topic_pb.js";
import { type RuntimeProxy, serveRuntimeProxy } from "../testing/runtime-proxy.js";

const declareMock = vi.hoisted(() => vi.fn((_req: unknown) => Promise.resolve({})));

vi.mock("../utils/rpc", () => ({
  rpc: { resource: { declare: declareMock } },
}));

const { topic, UnprovisionedResourceError } = await import("./index.js");
const { worker } = await import("../worker/index.js");

describe("topic discovery declare", () => {
  beforeEach(() => {
    declareMock.mockClear();
  });

  it("declares a TOPIC under its own name with every option unset when none is given", () => {
    topic("orders");

    expect(declareMock).toHaveBeenCalledWith({
      resource: { name: "orders", type: ResourceType.TOPIC },
      config: { case: "topic", value: { schema: "", ordered: false, retry: undefined } },
      source: expect.any(String),
    });
  });

  it("declares a topic's schema, ordering and retry", () => {
    topic("payments", {
      schema: z.object({ amount: z.number() }),
      ordered: true,
      retry: { maxAttempts: 10 },
    });

    const [request] = declareMock.mock.calls[0] as [
      { config: { value: { schema: string; ordered: boolean; retry: unknown } } },
    ];
    expect(JSON.parse(request.config.value.schema)).toMatchObject({
      type: "object",
      properties: { amount: { type: "number" } },
    });
    expect(request.config.value.ordered).toBe(true);
    expect(request.config.value.retry).toEqual({
      maxAttempts: 10,
      minDelay: undefined,
      maxDelay: undefined,
    });
  });

  it("declares a consumer of the topic with every option it was given", () => {
    const billing = worker("billing");

    topic("invoices").consumer("send-invoice", async () => {}, {
      retry: { maxAttempts: 2, maxDelay: 30 },
      concurrency: 4,
      lanes: ["high", "low"],
      maxDuration: "5m",
      worker: billing,
    });

    expect(declareMock).toHaveBeenCalledWith({
      resource: { name: "send-invoice", type: ResourceType.CONSUMER },
      config: {
        case: "consumer",
        value: {
          topic: "invoices",
          worker: "billing",
          retry: { maxAttempts: 2, minDelay: undefined, maxDelay: { seconds: 30n, nanos: 0 } },
          concurrency: 4,
          maxDuration: { seconds: 300n, nanos: 0 },
          lanes: [Lane.HIGH, Lane.LOW],
          batch: undefined,
        },
      },
      source: expect.any(String),
    });
  });

  it("declares a batch consumer's batch size and timeout", () => {
    topic("events").batchConsumer("rollup", async () => {}, {
      batchSize: 500,
      batchTimeout: "10s",
    });

    expect(declareMock).toHaveBeenCalledWith({
      resource: { name: "rollup", type: ResourceType.CONSUMER },
      config: {
        case: "consumer",
        value: {
          topic: "events",
          worker: "",
          retry: undefined,
          concurrency: 0,
          maxDuration: undefined,
          lanes: [],
          batch: { size: 500, timeout: { seconds: 10n, nanos: 0 } },
        },
      },
      source: expect.any(String),
    });
  });

  it("declares a WORKER with its concurrency", () => {
    worker("media", { concurrency: 8, onStart: () => {} });

    expect(declareMock).toHaveBeenCalledWith({
      resource: { name: "media", type: ResourceType.WORKER },
      config: { case: "worker", value: { concurrency: 8 } },
      source: expect.any(String),
    });
  });

  it("refuses a lane it does not know, naming it", () => {
    expect(() =>
      topic("bad-lanes").consumer("c", async () => {}, { lanes: ["urgent" as never] }),
    ).toThrow('"urgent" is not a lane');
  });
});

describe("a topic at runtime", () => {
  let proxy: RuntimeProxy;
  const sends: SendRequest[] = [];

  beforeEach(async () => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv(
      "OCEL_RESOURCE_TOPIC_orders",
      JSON.stringify({ name: "orders", topic: { topic: "shop-prod-orders" } }),
    );
    sends.length = 0;
    proxy = await serveRuntimeProxy((router) =>
      router.service(TopicService, {
        send: (req) => {
          sends.push(req);
          return { messageId: "01J00000000000000000000000" };
        },
      }),
    );
  });

  afterEach(async () => {
    vi.unstubAllEnvs();
    await proxy.close();
  });

  it("sends the payload as JSON to its bound topic, and answers the message id", async () => {
    const orders = topic<{ id: string }>("orders");

    expect(await orders.send({ id: "o1" })).toBe("01J00000000000000000000000");
    expect(sends[0]?.topic).toBe("shop-prod-orders");
    expect(new TextDecoder().decode(sends[0]?.payload)).toBe('{"id":"o1"}');
    expect(proxy.authorizations).toEqual(["Bearer session-token"]);
  });

  it("sends every send option, the delay as the time the message is due", async () => {
    vi.useFakeTimers({ now: new Date("2026-03-01T00:00:00Z"), toFake: ["Date"] });
    await topic("orders").send(1, {
      delay: 90,
      idempotencyKey: "order-1",
      key: "customer-1",
      lane: "low",
    });
    vi.useRealTimers();

    expect(timestampDate(sends[0]?.dueAt as Timestamp)).toEqual(new Date("2026-03-01T00:01:30Z"));
    expect(sends[0]).toMatchObject({
      idempotencyKey: "order-1",
      key: "customer-1",
      lane: Lane.LOW,
    });
  });

  it("refuses a payload over 256 KiB before sending anything", async () => {
    await expect(topic("orders").send("x".repeat(300_000))).rejects.toThrow("256 KiB");
    expect(sends).toHaveLength(0);
  });
});

describe("a topic during discovery", () => {
  it("refuses to send, naming the topic and the operation", async () => {
    const orders = topic("orders");

    await expect(orders.send({})).rejects.toThrow(
      "'topic(\"orders\")' cannot be used during discovery: tried to access 'send'",
    );
    await expect(orders.send({})).rejects.toThrow(UnprovisionedResourceError);
  });

  it("refuses to read dead letters, naming the topic and the operation", async () => {
    await expect(topic("orders").deadLetter("bill").count()).rejects.toThrow(
      "tried to access 'deadLetter.count'",
    );
  });
});

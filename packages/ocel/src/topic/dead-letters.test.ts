import { create } from "@bufbuild/protobuf";
import { timestampFromDate, ValueSchema } from "@bufbuild/protobuf/wkt";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TopicService } from "../gen/proto/app/topic/v1/topic_pb.js";
import { type RuntimeProxy, serveRuntimeProxy } from "../testing/runtime-proxy.js";

vi.mock("../utils/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { topic } = await import("./index.js");

describe("a consumer's dead letters", () => {
  let proxy: RuntimeProxy;
  const requests: unknown[] = [];

  beforeEach(async () => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv(
      "OCEL_RESOURCE_TOPIC_orders",
      JSON.stringify({ name: "orders", topic: { topic: "shop-prod-orders" } }),
    );
    requests.length = 0;
    proxy = await serveRuntimeProxy((router) =>
      router.service(TopicService, {
        listDeadLetters: (req) => {
          requests.push(["list", req.topic, req.consumer, req.cursor, req.limit]);
          return {
            deadLetters: [
              {
                execution: "01J00000000000000000000000-bill",
                message: {
                  id: "01J00000000000000000000000",
                  publishedAt: timestampFromDate(new Date("2026-01-01T00:00:00Z")),
                },
                payload: create(ValueSchema, { kind: { case: "numberValue", value: 7 } }),
                attempts: 3,
                error: "card declined",
                failedAt: timestampFromDate(new Date("2026-01-01T00:05:00Z")),
              },
            ],
            nextCursor: "",
          };
        },
        redriveDeadLetters: (req) => {
          requests.push(["redrive", req.topic, req.consumer, req.executions]);
          return { redriven: 2n };
        },
        purgeDeadLetters: (req) => {
          requests.push(["purge", req.topic, req.consumer, req.executions]);
          return { purged: 1n };
        },
        countDeadLetters: (req) => {
          requests.push(["count", req.topic, req.consumer]);
          return { count: 4n };
        },
      }),
    );
  });

  afterEach(async () => {
    vi.unstubAllEnvs();
    await proxy.close();
  });

  it("lists a page of dead letters of the consumer on the bound topic", async () => {
    const page = await topic("orders").deadLetter("bill").list({ cursor: "c", limit: 10 });

    expect(page).toEqual({
      deadLetters: [
        {
          execution: "01J00000000000000000000000-bill",
          messageId: "01J00000000000000000000000",
          publishedAt: new Date("2026-01-01T00:00:00Z"),
          payload: 7,
          attempts: 3,
          error: "card declined",
          failedAt: new Date("2026-01-01T00:05:00Z"),
        },
      ],
      nextCursor: "",
    });
    expect(requests).toEqual([["list", "shop-prod-orders", "bill", "c", 10]]);
  });

  it("redrives, purges and counts, answering how many", async () => {
    const orders = topic("orders");
    const bill = orders.consumer("bill", async () => {});
    const letters = orders.deadLetter(bill);

    expect(await letters.redrive()).toBe(2);
    expect(await letters.purge(["e1"])).toBe(1);
    expect(await letters.count()).toBe(4);
    expect(requests).toEqual([
      ["redrive", "shop-prod-orders", "bill", []],
      ["purge", "shop-prod-orders", "bill", ["e1"]],
      ["count", "shop-prod-orders", "bill"],
    ]);
  });
});

import { describe, expectTypeOf, it } from "vitest";
import { z } from "zod";
import { createRealtimeClient, type EventMeta } from "./client.js";
import { realtime } from "./realtime.js";

const OrderEvent = z.object({ status: z.string() });
const Shipped = z.object({ at: z.string().transform((s) => new Date(s)) });

const rt = realtime("app", {
  authorize: async () => ({ userId: "u1" }),
  channels: {
    "orders/:orderId": { schema: OrderEvent, subscribe: () => true },
    "projects/:projectId/deploys/:deployId": {
      schema: OrderEvent,
      wildcard: true,
      subscribe: () => true,
    },
    "rooms/:roomId": { schema: Shipped, subscribe: () => true, publish: () => true },
    status: { schema: OrderEvent, subscribe: "public" },
  },
});

const live = createRealtimeClient<typeof rt>({ url: "/api/realtime" });

describe("a realtime client typed from a declared resource", () => {
  it("hands each subscriber its schema's output and the envelope's meta", () => {
    live.subscribe("orders/:orderId", { params: { orderId: "o1" } }, (event, meta) => {
      expectTypeOf(event).toEqualTypeOf<{ status: string }>();
      expectTypeOf(meta).toEqualTypeOf<EventMeta>();
    });
    live.subscribe("status", {}, (event) => {
      expectTypeOf(event).toEqualTypeOf<{ status: string }>();
    });
  });

  it("requires every param of a pattern that is not wildcard", () => {
    // @ts-expect-error orderId is required
    live.subscribe("orders/:orderId", { params: {} }, () => {});
    // @ts-expect-error params are required
    live.subscribe("orders/:orderId", {}, () => {});
    // @ts-expect-error no such param
    live.subscribe("orders/:orderId", { params: { orderId: "o1", other: "x" } }, () => {});
  });

  it("lets a wildcard subscriber leave off trailing params only", () => {
    live.subscribe("projects/:projectId/deploys/:deployId", {}, () => {});
    live.subscribe(
      "projects/:projectId/deploys/:deployId",
      { params: { projectId: "p1" } },
      () => {},
    );
    live.subscribe(
      "projects/:projectId/deploys/:deployId",
      { params: { projectId: "p1", deployId: "d1" } },
      () => {},
    );
    live.subscribe(
      "projects/:projectId/deploys/:deployId",
      // @ts-expect-error a leading param cannot be left off
      { params: { deployId: "d1" } },
      () => {},
    );
  });

  it("publishes a schema's input where a publish rule exists, filling every param", () => {
    void live.publish("rooms/:roomId", { params: { roomId: "r1" }, body: { at: "2026-01-01" } });
    // @ts-expect-error the body is the schema's input
    void live.publish("rooms/:roomId", { params: { roomId: "r1" }, body: { at: new Date() } });
    // @ts-expect-error roomId is required
    void live.publish("rooms/:roomId", { params: {}, body: { at: "x" } });
  });

  it("refuses a publish on a pattern with no publish rule, and a pattern never declared", () => {
    // @ts-expect-error only the server publishes on orders
    void live.publish("orders/:orderId", { params: { orderId: "o1" }, body: { status: "x" } });
    // @ts-expect-error no such pattern
    live.subscribe("nope", {}, () => {});
  });
});

type Generated = {
  "orders/:orderId": { event: { status: string } };
  "projects/:projectId": { event: { name: string }; wildcard: true };
  "rooms/:roomId": { event: { text: string }; publish: { text: string } };
};

describe("a realtime client typed from generated channel types", () => {
  const generated = createRealtimeClient<Generated>({ url: "/api/realtime" });

  it("types subscribe and publish the same way", () => {
    generated.subscribe("orders/:orderId", { params: { orderId: "o1" } }, (event) => {
      expectTypeOf(event).toEqualTypeOf<{ status: string }>();
    });
    generated.subscribe("projects/:projectId", {}, () => {});
    void generated.publish("rooms/:roomId", { params: { roomId: "r1" }, body: { text: "hi" } });
    // @ts-expect-error orderId is required
    generated.subscribe("orders/:orderId", { params: {} }, () => {});
    // @ts-expect-error only the server publishes on orders
    void generated.publish("orders/:orderId", { params: { orderId: "o1" }, body: { status: "x" } });
  });
});

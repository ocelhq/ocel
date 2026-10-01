import { describe, expectTypeOf, it } from "vitest";
import { z } from "zod";
import { realtime } from "./realtime.js";

const OrderEvent = z.object({ status: z.string() });
const Shipped = z.object({ at: z.string().transform((s) => new Date(s)) });

const rt = realtime("app", {
  authorize: async (request) => {
    expectTypeOf(request).toEqualTypeOf<Request>();
    return request.headers.get("x-user") ? { userId: "u1" } : null;
  },
  channels: {
    "orders/:orderId": {
      schema: OrderEvent,
      subscribe: async ({ auth, params, request }) => {
        expectTypeOf(auth).toEqualTypeOf<{ userId: string }>();
        expectTypeOf(params).toEqualTypeOf<{ orderId: string }>();
        expectTypeOf(request).toEqualTypeOf<Request>();
        return true;
      },
    },
    "projects/:projectId/deploys/:deployId": {
      schema: Shipped,
      wildcard: true,
      subscribe: ({ params }) => {
        expectTypeOf(params).toEqualTypeOf<{ projectId?: string; deployId?: string }>();
        return true;
      },
    },
    "rooms/:roomId": {
      schema: Shipped,
      subscribe: () => true,
      publish: ({ auth, params, body }) => {
        expectTypeOf(auth).toEqualTypeOf<{ userId: string }>();
        expectTypeOf(params).toEqualTypeOf<{ roomId: string }>();
        expectTypeOf(body).toEqualTypeOf<{ at: Date }>();
        return true;
      },
    },
    status: { schema: OrderEvent, subscribe: "public" },
  },
});

describe("a realtime resource typed from its channels", () => {
  it("publishes a pattern's params and its schema's input", () => {
    void rt.publish("orders/:orderId", { params: { orderId: "o1" }, body: { status: "x" } });
    void rt.publish("rooms/:roomId", { params: { roomId: "r1" }, body: { at: "2026-01-01" } });
    void rt.publish("status", { body: { status: "up" } });
  });

  it("refuses a publish missing a param, even on a wildcard pattern", () => {
    // @ts-expect-error orderId is required
    void rt.publish("orders/:orderId", { params: {}, body: { status: "x" } });
    void rt.publish("projects/:projectId/deploys/:deployId", {
      // @ts-expect-error a publish fills every param
      params: { projectId: "p1" },
      body: { at: "x" },
    });
  });

  it("refuses a body its schema does not take, and a pattern never declared", () => {
    // @ts-expect-error status is a string
    void rt.publish("orders/:orderId", { params: { orderId: "o1" }, body: { status: 1 } });
    // @ts-expect-error no such pattern
    void rt.publish("nope", { body: {} });
  });
});

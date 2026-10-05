import { describe, expectTypeOf, it } from "vitest";
import { z } from "zod";
import type { JsonText } from "../delivery/json-text.js";
import { type Topic, topic } from "./topic.js";

describe("a topic's payload typed from its schema", () => {
  const orders = topic("orders", {
    schema: z.object({ id: z.string(), total: z.string().transform(Number) }),
  });

  it("sends the schema's input, and hands consumers its output", () => {
    expectTypeOf(orders).toEqualTypeOf<
      Topic<{ id: string; total: string }, { id: string; total: number }>
    >();
    orders.consumer("bill", (payload) => {
      expectTypeOf(payload).toEqualTypeOf<{ id: string; total: number }>();
    });
    orders.batchConsumer(
      "report",
      (payloads) => {
        expectTypeOf(payloads).toEqualTypeOf<{ id: string; total: number }[]>();
      },
      { batchSize: 10 },
    );
  });

  it("refuses to send a payload of another type", () => {
    // @ts-expect-error the total is sent as a string
    void orders.send({ id: "o1", total: 3 });
  });
});

describe("a topic typed by its type argument", () => {
  it("sends that type or JSON text, and consumes that type", () => {
    const clicks = topic<{ x: number }>("clicks");

    expectTypeOf(clicks.send).parameter(0).toEqualTypeOf<{ x: number } | JsonText>();
    clicks.consumer("count", (payload, { ctx, payloadJson }) => {
      expectTypeOf(payload).toEqualTypeOf<{ x: number }>();
      expectTypeOf(ctx.kind).toEqualTypeOf<"task" | "consumer">();
      expectTypeOf(payloadJson).toEqualTypeOf<JsonText>();
    });
  });

  it("sends anything without one", () => {
    expectTypeOf(topic("raw").send).parameter(0).toEqualTypeOf<unknown>();
  });
});

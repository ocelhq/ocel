import { describe, expectTypeOf, it } from "vitest";
import { z } from "zod";
import type { JsonText } from "../delivery/json-text.js";
import type { RunContext } from "../worker/context.js";
import { type Task, task } from "./task.js";

const image = z.object({ url: z.string(), width: z.string().transform(Number) });

describe("a task's payload typed from its schema", () => {
  const resize = task("resize", {
    schema: image,
    run: async (payload, { ctx, signal, payloadJson }) => {
      expectTypeOf(payload).toEqualTypeOf<{ url: string; width: number }>();
      expectTypeOf(ctx).toEqualTypeOf<RunContext>();
      expectTypeOf(signal).toEqualTypeOf<AbortSignal>();
      expectTypeOf(payloadJson).toEqualTypeOf<JsonText>();
      return { bytes: payload.width * 2 };
    },
    onSuccess: ({ payload, output }) => {
      expectTypeOf(payload).toEqualTypeOf<{ url: string; width: number }>();
      expectTypeOf(output).toEqualTypeOf<{ bytes: number }>();
    },
  });

  it("triggers with the schema's input, or JSON text", () => {
    expectTypeOf(resize.trigger)
      .parameter(0)
      .toEqualTypeOf<{ url: string; width: string } | JsonText>();
    expectTypeOf(resize).toEqualTypeOf<Task<{ url: string; width: string }, { bytes: number }>>();
  });

  it("hands a batch run the list of the schema's outputs", () => {
    task("resize-many", {
      schema: image,
      batch: { size: 10 },
      run: async (payloads) => {
        expectTypeOf(payloads).toEqualTypeOf<{ url: string; width: number }[]>();
      },
    });
  });
});

describe("a task's payload typed from its run", () => {
  it("triggers with the type run's first parameter is annotated with", () => {
    const send = task("send-email", {
      run: async (email: { to: string }) => email.to.length,
    });

    expectTypeOf(send).toEqualTypeOf<Task<{ to: string }, number>>();
  });

  it("triggers a batch task with one item of the list its run takes, or JSON text", () => {
    const rollup = task("rollup", {
      batch: { size: 100 },
      run: async (events: { kind: string }[]) => events.length,
    });

    expectTypeOf(rollup.trigger).parameter(0).toEqualTypeOf<{ kind: string } | JsonText>();
  });

  it("takes any payload when run declares none", () => {
    const tick = task("tick", { run: async () => {} });

    expectTypeOf(tick.trigger).parameter(0).toEqualTypeOf<unknown>();
  });

  it("refuses a payload of another type", () => {
    const send = task("send-email-strict", { run: async (_email: { to: string }) => {} });

    // @ts-expect-error a number is not an email
    void send.trigger(1);
  });

  it("takes a catchError that answers nothing, or whether to skip retrying", () => {
    task("caught-sync", { run: async () => {}, catchError: () => {} });
    task("caught-async", { run: async () => {}, catchError: async () => {} });
    task("caught-skip", {
      run: async () => {},
      catchError: async ({ error }) =>
        error instanceof TypeError ? { skipRetrying: true } : undefined,
    });
  });

  it("refuses a lane it does not know", () => {
    const send = task("send-email-lane", { run: async () => {} });

    // @ts-expect-error "urgent" is not a lane
    void send.trigger(1, { lane: "urgent" });
  });
});

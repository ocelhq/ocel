import { task } from "ocel/task";
import { topic } from "ocel/topic";
import { worker } from "ocel/worker";

let starts = 0;
let wrappedBy = "";
let audited = "";

export const background = worker("worker", {
  onStart: () => {
    starts += 1;
  },
  middleware: async ({ ctx, next }) => {
    wrappedBy = `${ctx.kind}:${ctx.name}`;
    await next();
  },
});

export const greet = task("greet", {
  worker: background,
  run: (payload: { name: string }) => ({
    greeting: `hello ${payload.name}`,
    starts,
    wrappedBy,
    audited,
  }),
});

export const orders = topic<{ name: string }>("orders");

export const audit = orders.consumer(
  "audit",
  (payload) => {
    audited = `${wrappedBy} ${payload.name}`;
  },
  { worker: background },
);

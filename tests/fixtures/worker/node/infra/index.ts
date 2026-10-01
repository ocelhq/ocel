import { task } from "ocel/task";
import { worker } from "ocel/worker";

let starts = 0;
let wrappedBy = "";

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
  run: (payload: { name: string }) => ({ greeting: `hello ${payload.name}`, starts, wrappedBy }),
});

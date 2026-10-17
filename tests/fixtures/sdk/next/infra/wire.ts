import { type RunOptions, task } from "ocel/task";
import { topic } from "ocel/topic";

export type Sighting = { kind: string; name: string; topic: string; payload: string };

export const sighting = task("sighting", { run: (seen: Sighting) => seen });

async function recordSighting(tag: string, { ctx, payloadJson }: RunOptions): Promise<void> {
  await sighting.trigger(
    { kind: ctx.kind, name: ctx.name, topic: ctx.topic, payload: payloadJson.text },
    { tags: [tag] },
  );
}

export const verbatim = task("verbatim", {
  run: async (_payload: unknown, options) => {
    await recordSighting(options.ctx.id, options);
    return options.payloadJson;
  },
});

export const notices = topic<unknown>("notices");

export const noticeLog = notices.consumer("notice-log", (_payload, options) =>
  recordSighting(options.ctx.message.id, options),
);

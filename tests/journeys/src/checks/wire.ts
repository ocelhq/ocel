import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { type Check, type CheckContext, json } from "./context";
import { ENDED, type RunRecord, readAnswer, waitFor } from "./runs";

export const EXACT_JSON = '{"ratio":2.0,"id":9007199254740993,"count":2}';

type DeclaredNames = { task: string; topic: string; consumer: string };

type RunRecordText = Omit<RunRecord, "payload" | "output"> & { payload: string; output: string };

type Sighting = { kind: string; name: string; topic: string; payload: string };

async function postRaw<T>(ctx: CheckContext, at: string, body: string): Promise<T> {
  const reply = await json(ctx, at, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body,
  });
  return readAnswer<T>(reply, `the post to ${at}`);
}

function triggerWith(ctx: CheckContext, payload: string): Promise<{ id: string }> {
  return postRaw(ctx, "/api/wire/trigger", payload);
}

function sendWith(ctx: CheckContext, payload: string): Promise<{ messageId: string }> {
  return postRaw(ctx, "/api/wire/send", payload);
}

function waitForSighting(ctx: CheckContext, tag: string): Promise<Sighting> {
  return waitFor(`the handler recording what it was handed under ${tag}`, async () => {
    const reply = await json(ctx, `/api/wire/sightings/${tag}`);
    const [sighting] = readAnswer<Sighting[]>(reply, "the sightings");
    return sighting;
  });
}

function waitForRunText(ctx: CheckContext, id: string): Promise<RunRecordText> {
  return waitFor(`run ${id} ending`, async () => {
    const reply = await json(ctx, `/api/wire/runs/${id}`);
    const run = readAnswer<RunRecordText>(reply, "the retrieve");
    return ENDED.includes(run.status) ? run : undefined;
  });
}

function newProbePayload(): string {
  return JSON.stringify({ probe: randomUUID() });
}

function declaredTaskNameCheck(task: string): Check {
  return {
    title:
      "a task's handler runs as the task it was declared as, under that name as task and topic",
    run: async (ctx) => {
      const { id } = await triggerWith(ctx, newProbePayload());
      const sighting = await waitForSighting(ctx, id);
      assert.deepEqual(
        { kind: sighting.kind, name: sighting.name, topic: sighting.topic },
        { kind: "task", name: task, topic: task },
      );
    },
  };
}

function declaredTopicNameCheck(topic: string, consumer: string): Check {
  return {
    title:
      "a consumer's handler runs as the consumer it was declared as, of the topic it was declared on",
    run: async (ctx) => {
      const { messageId } = await sendWith(ctx, newProbePayload());
      const sighting = await waitForSighting(ctx, messageId);
      assert.deepEqual(
        { kind: sighting.kind, name: sighting.name, topic: sighting.topic },
        { kind: "consumer", name: consumer, topic },
      );
    },
  };
}

export const exactTaskPayloadCheck: Check = {
  title:
    "a task's JSON payload reaches its handler and its run record byte for byte, and its output comes back the same",
  run: async (ctx) => {
    const { id } = await triggerWith(ctx, EXACT_JSON);
    const sighting = await waitForSighting(ctx, id);
    assert.equal(sighting.payload, EXACT_JSON, `the handler was handed ${sighting.payload}`);
    const run = await waitForRunText(ctx, id);
    assert.equal(run.status, "COMPLETED");
    assert.equal(run.payload, EXACT_JSON, `the run record reads back the payload ${run.payload}`);
    assert.equal(run.output, EXACT_JSON, `the run record reads back the output ${run.output}`);
  },
};

export const exactTopicPayloadCheck: Check = {
  title: "a message's JSON payload reaches its consumer byte for byte",
  run: async (ctx) => {
    const { messageId } = await sendWith(ctx, EXACT_JSON);
    const sighting = await waitForSighting(ctx, messageId);
    assert.equal(sighting.payload, EXACT_JSON, `the consumer was handed ${sighting.payload}`);
  },
};

export function tasksWireChecks(declared: DeclaredNames): Check[] {
  return [
    declaredTaskNameCheck(declared.task),
    declaredTopicNameCheck(declared.topic, declared.consumer),
    exactTaskPayloadCheck,
    exactTopicPayloadCheck,
  ];
}

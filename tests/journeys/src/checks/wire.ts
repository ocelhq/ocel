import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { type Check, type CheckContext, json } from "./context";
import { ENDED, type RunRecord, readAnswer, waitFor } from "./runs";

export const EXACT_JSON = '{"ratio":2.0,"id":9007199254740993,"count":2}';

type Names = { declared: string; bound: string; consumer?: string };

type WireNames = { task: Names; topic: Names };

type SentRequest = { procedure: string; name: string; payload: string };

type TriggerAnswer = { id: string; error: string; requests: SentRequest[] };

type SendAnswer = { messageId: string; error: string; requests: SentRequest[] };

type RunRecordText = Omit<RunRecord, "payload" | "output"> & { payload: string; output: string };

type Sighting = { kind: string; name: string; topic: string; payload: string };

type Envelope = { topic: string; consumer: string; payload: unknown };

async function readNames(ctx: CheckContext): Promise<WireNames> {
  const names = readAnswer<WireNames>(await json(ctx, "/api/wire/names"), "the names");
  for (const { declared, bound } of [names.task, names.topic]) {
    assert.notEqual(
      bound,
      declared,
      `${declared} is bound under its declared name, so a request naming either proves nothing`,
    );
  }
  return names;
}

async function postRaw<T>(ctx: CheckContext, at: string, body: string): Promise<T> {
  const reply = await json(ctx, at, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body,
  });
  return readAnswer<T>(reply, `the post to ${at}`);
}

function findRequest(requests: SentRequest[], procedure: string): SentRequest {
  const found = requests.filter((request) => request.procedure.endsWith(`/${procedure}`));
  assert.equal(found.length, 1, `the SDK sent ${JSON.stringify(requests)}, not one ${procedure}`);
  return found[0] as SentRequest;
}

function waitForSighting(ctx: CheckContext, tag: string): Promise<Sighting> {
  return waitFor(`the handler recording what it was handed under ${tag}`, async () => {
    const reply = await json(ctx, `/api/wire/sightings/${tag}`);
    const [sighting] = readAnswer<Sighting[]>(reply, "the sightings");
    return sighting;
  });
}

async function waitForEnvelopes(ctx: CheckContext, tag: string): Promise<[string, Envelope][]> {
  const texts = await waitFor(
    `the worker recording the envelope it was delivered under ${tag}`,
    async () => {
      const reply = await json(ctx, `/api/wire/envelopes/${tag}`);
      const found = readAnswer<string[]>(reply, "the envelopes");
      return found.length > 0 ? found : undefined;
    },
  );
  return texts.map((text) => [text, JSON.parse(text) as Envelope]);
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

function assertTaken(answer: { error: string }, what: string): void {
  assert.equal(answer.error, "", `the ${what} was refused: ${answer.error}`);
}

export const declaredTaskNameCheck: Check = {
  title:
    "a trigger leaves the SDK naming the task as declared though its binding names another, and its envelope reaches the worker naming the task as topic and consumer",
  run: async (ctx) => {
    const { task } = await readNames(ctx);
    const answer = await postRaw<TriggerAnswer>(ctx, "/api/wire/trigger", newProbePayload());
    const request = findRequest(answer.requests, "Trigger");
    assert.equal(request.name, task.declared, `the trigger left the SDK naming ${request.name}`);
    assertTaken(answer, "trigger");
    for (const [text, envelope] of await waitForEnvelopes(ctx, answer.id)) {
      assert.deepEqual(
        { topic: envelope.topic, consumer: envelope.consumer },
        { topic: task.declared, consumer: task.declared },
        `the envelope names another task: ${text}`,
      );
    }
    const sighting = await waitForSighting(ctx, answer.id);
    assert.deepEqual(
      { kind: sighting.kind, name: sighting.name, topic: sighting.topic },
      { kind: "task", name: task.declared, topic: task.declared },
    );
  },
};

export const declaredTopicNameCheck: Check = {
  title:
    "a send leaves the SDK naming the topic as declared though its binding names another, and its envelope reaches the worker naming the declared topic and consumer",
  run: async (ctx) => {
    const { topic } = await readNames(ctx);
    const answer = await postRaw<SendAnswer>(ctx, "/api/wire/send", newProbePayload());
    const request = findRequest(answer.requests, "Send");
    assert.equal(request.name, topic.declared, `the send left the SDK naming ${request.name}`);
    assertTaken(answer, "send");
    for (const [text, envelope] of await waitForEnvelopes(ctx, answer.messageId)) {
      assert.deepEqual(
        { topic: envelope.topic, consumer: envelope.consumer },
        { topic: topic.declared, consumer: topic.consumer },
        `the envelope names another topic or consumer: ${text}`,
      );
    }
    const sighting = await waitForSighting(ctx, answer.messageId);
    assert.deepEqual(
      { kind: sighting.kind, name: sighting.name, topic: sighting.topic },
      { kind: "consumer", name: topic.consumer, topic: topic.declared },
    );
  },
};

async function assertInlined(ctx: CheckContext, tag: string): Promise<void> {
  for (const [text, envelope] of await waitForEnvelopes(ctx, tag)) {
    assert.equal(
      typeof envelope.payload,
      "object",
      `the envelope carries its payload as a ${typeof envelope.payload}, not as the JSON value: ${text}`,
    );
    assert.ok(
      text.includes(EXACT_JSON),
      `the envelope does not carry the payload's JSON text verbatim: ${text}`,
    );
  }
}

export const exactTaskPayloadCheck: Check = {
  title:
    "a task's JSON payload leaves the SDK, reaches the worker inlined in its envelope, reaches its handler and its run record byte for byte, and its output comes back the same",
  run: async (ctx) => {
    const answer = await postRaw<TriggerAnswer>(ctx, "/api/wire/trigger", EXACT_JSON);
    const request = findRequest(answer.requests, "Trigger");
    assert.equal(
      request.payload,
      EXACT_JSON,
      `the trigger left the SDK with the payload ${request.payload}`,
    );
    assertTaken(answer, "trigger");
    await assertInlined(ctx, answer.id);
    const sighting = await waitForSighting(ctx, answer.id);
    assert.equal(sighting.payload, EXACT_JSON, `the handler was handed ${sighting.payload}`);
    const run = await waitForRunText(ctx, answer.id);
    assert.equal(run.status, "COMPLETED");
    assert.equal(run.payload, EXACT_JSON, `the run record reads back the payload ${run.payload}`);
    assert.equal(run.output, EXACT_JSON, `the run record reads back the output ${run.output}`);
  },
};

export const exactTopicPayloadCheck: Check = {
  title:
    "a message's JSON payload leaves the SDK, reaches the worker inlined in its envelope, and reaches its consumer byte for byte",
  run: async (ctx) => {
    const answer = await postRaw<SendAnswer>(ctx, "/api/wire/send", EXACT_JSON);
    const request = findRequest(answer.requests, "Send");
    assert.equal(
      request.payload,
      EXACT_JSON,
      `the send left the SDK with the payload ${request.payload}`,
    );
    assertTaken(answer, "send");
    await assertInlined(ctx, answer.messageId);
    const sighting = await waitForSighting(ctx, answer.messageId);
    assert.equal(sighting.payload, EXACT_JSON, `the consumer was handed ${sighting.payload}`);
  },
};

export const tasksWireChecks: Check[] = [
  declaredTaskNameCheck,
  declaredTopicNameCheck,
  exactTaskPayloadCheck,
  exactTopicPayloadCheck,
];

import assert from "node:assert/strict";
import { setTimeout as delay } from "node:timers/promises";
import { type Check, type CheckContext, json } from "./context";

export const EXACT_JSON = '{"ratio":2.0,"id":9007199254740993,"count":2}';

const SETTLED_WITHIN_MS = 60_000;
const POLL_MS = 250;

type Sent = { res: Response; text: string; body: unknown };

type ShownRun = { id: string; task: string; status: string; payload: string; output: string };

type Seen = { kind: string; name: string; topic: string; payload: string };

function readAnswer<T>(sent: Sent, what: string): T {
  assert.equal(sent.res.status, 200, `${what} answered ${sent.res.status} ${sent.text}`);
  return sent.body as T;
}

function postExact(ctx: CheckContext, at: string): Promise<Sent> {
  return json(ctx, at, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: EXACT_JSON,
  });
}

async function waitFor<T>(what: string, read: () => Promise<T | undefined>): Promise<T> {
  const deadline = Date.now() + SETTLED_WITHIN_MS;
  for (;;) {
    const found = await read();
    if (found !== undefined) {
      return found;
    }
    assert.ok(Date.now() < deadline, `${what} within ${SETTLED_WITHIN_MS}ms`);
    await delay(POLL_MS);
  }
}

function waitForSeen(ctx: CheckContext, tag: string): Promise<Seen> {
  return waitFor(`the handler never recorded what it was handed under ${tag}`, async () => {
    const [seen] = readAnswer<Seen[]>(await json(ctx, `/api/seen/${tag}`), "the seen listing");
    return seen;
  });
}

export const exactTaskPayloadCheck: Check = {
  title:
    "a task's JSON payload reaches its handler and its run record byte for byte, and its output comes back the same, under the task's declared name",
  run: async (ctx) => {
    const { id } = readAnswer<{ id: string }>(
      await postExact(ctx, "/api/tasks/exact-echo/trigger"),
      "the trigger",
    );
    const run = await waitFor(`run ${id} never ended`, async () => {
      const shown = readAnswer<ShownRun>(await json(ctx, `/api/runs/${id}`), "the retrieve");
      return ["QUEUED", "DELAYED", "EXECUTING"].includes(shown.status) ? undefined : shown;
    });
    assert.equal(run.status, "COMPLETED");
    assert.equal(run.task, "exact-echo");
    assert.equal(run.payload, EXACT_JSON);
    assert.equal(run.output, EXACT_JSON);
    assert.deepEqual(await waitForSeen(ctx, id), {
      kind: "task",
      name: "exact-echo",
      topic: "exact-echo",
      payload: EXACT_JSON,
    });
  },
};

export const exactTopicPayloadCheck: Check = {
  title:
    "a message's JSON payload reaches its consumer byte for byte, in an envelope naming the declared topic and consumer",
  run: async (ctx) => {
    const { messageId } = readAnswer<{ messageId: string }>(
      await postExact(ctx, "/api/topics/exact-orders/send"),
      "the send",
    );
    assert.deepEqual(await waitForSeen(ctx, messageId), {
      kind: "consumer",
      name: "exact-audit",
      topic: "exact-orders",
      payload: EXACT_JSON,
    });
  },
};

export const tasksWireChecks: Check[] = [exactTaskPayloadCheck, exactTopicPayloadCheck];

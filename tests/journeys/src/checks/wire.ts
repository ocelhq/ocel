import assert from "node:assert/strict";
import { type Check, type CheckContext, json } from "./context";
import { type Reply, type RunRecord, readAnswer, waitFor } from "./runs";

export const EXACT_JSON = '{"ratio":2.0,"id":9007199254740993,"count":2}';

type RunRecordText = Omit<RunRecord, "payload" | "output"> & { payload: string; output: string };

type Sighting = { kind: string; name: string; topic: string; payload: string };

function postExact(ctx: CheckContext, at: string): Promise<Reply> {
  return json(ctx, at, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: EXACT_JSON,
  });
}

function waitForSeen(ctx: CheckContext, tag: string): Promise<Sighting> {
  return waitFor(`the handler never recorded what it was handed under ${tag}`, async () => {
    const [sighting] = readAnswer<Sighting[]>(
      await json(ctx, `/api/seen/${tag}`),
      "the seen listing",
    );
    return sighting;
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
      const shown = readAnswer<RunRecordText>(await json(ctx, `/api/runs/${id}`), "the retrieve");
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

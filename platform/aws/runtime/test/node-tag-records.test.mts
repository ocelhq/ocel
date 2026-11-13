import { afterEach, expect, test, vi } from "vitest";

afterEach(() => {
  vi.resetModules();
  vi.restoreAllMocks();
});

const env = {
  OCEL_STATE_TABLE: "state",
  OCEL_ISR_TAG_NAMESPACE: "PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#",
};

async function writerWithResponses(responses: unknown[]) {
  const sends: any[] = [];
  vi.doMock("@aws-sdk/client-dynamodb", async (orig) => {
    const actual = await orig<any>();
    return {
      ...actual,
      DynamoDBClient: class {
        async send(cmd: any) {
          sends.push(cmd.input);
          const next = responses.shift();
          if (next instanceof Error) throw next;
          return next ?? {};
        }
      },
    };
  });
  const { newTagRecordWriter } = await import("../src/node/tag-records.mjs");
  return { sends, newWriter: newTagRecordWriter };
}

test("writes nothing where the function was given no state table or tag namespace", async () => {
  const { newWriter } = await writerWithResponses([]);

  expect(newWriter({})).toBeUndefined();
  expect(newWriter({ OCEL_STATE_TABLE: "state" })).toBeUndefined();
  expect(newWriter({ OCEL_ISR_TAG_NAMESPACE: env.OCEL_ISR_TAG_NAMESPACE })).toBeUndefined();
});

test("records a purge as an expired tag and an invalidation as a stale one, in the app's own namespace", async () => {
  const { newWriter, sends } = await writerWithResponses([]);

  await newWriter(env)!([
    { tag: "posts", mode: "purge", raisedAt: 700 },
    { tag: "path:/blog/a", mode: "invalidate", raisedAt: 800 },
  ]);

  const byTag = Object.fromEntries(sends.map((input) => [input.Key.pk.S, input]));
  const posts = byTag[`${env.OCEL_ISR_TAG_NAMESPACE}posts`];
  expect(posts.TableName).toBe("state");
  expect(posts.ExpressionAttributeValues[":expired"]).toEqual({ N: "700" });
  expect(posts.ExpressionAttributeValues[":stale"]).toBeUndefined();
  const path = byTag[`${env.OCEL_ISR_TAG_NAMESPACE}path:/blog/a`];
  expect(path.ExpressionAttributeValues[":stale"]).toEqual({ N: "800" });
  expect(path.ExpressionAttributeValues[":expired"]).toBeUndefined();
});

test("takes a record a newer one already covers as written", async () => {
  const guard = Object.assign(new Error("covered"), { name: "ConditionalCheckFailedException" });
  const { newWriter } = await writerWithResponses([guard]);

  await expect(
    newWriter(env)!([{ tag: "posts", mode: "purge", raisedAt: 700 }]),
  ).resolves.toBeUndefined();
});

test("writes every tag it can, then reports the first one that failed", async () => {
  const { newWriter, sends } = await writerWithResponses([new Error("throttled")]);

  await expect(
    newWriter(env)!([
      { tag: "a", mode: "purge", raisedAt: 1 },
      { tag: "b", mode: "purge", raisedAt: 1 },
    ]),
  ).rejects.toThrow(/throttled/);
  expect(sends).toHaveLength(2);
});

test("keeps a bounded number of writes in flight however many tags one request revalidates", async () => {
  let inFlight = 0;
  let most = 0;
  vi.doMock("@aws-sdk/client-dynamodb", async (orig) => {
    const actual = await orig<any>();
    return {
      ...actual,
      DynamoDBClient: class {
        async send() {
          inFlight++;
          most = Math.max(most, inFlight);
          await new Promise((resolve) => setTimeout(resolve, 1));
          inFlight--;
          return {};
        }
      },
    };
  });
  const { newTagRecordWriter } = await import("../src/node/tag-records.mjs");

  await newTagRecordWriter(env)!(
    Array.from({ length: 200 }, (_, i) => ({ tag: `t${i}`, mode: "purge" as const, raisedAt: 1 })),
  );

  expect(most).toBeGreaterThan(1);
  expect(most).toBeLessThanOrEqual(8);
});

import { createHmac } from "node:crypto";
import type { Refresh } from "@framework/next-runtime/refresh";
import { expect, test, vi } from "vitest";
import { newRefreshQueue, type RefreshQueueOptions } from "../src/next/refresh-queue.mjs";
import {
  isRefreshTaskSignedBy,
  refreshSignatureHeader,
  signRefreshTask,
} from "../src/next/refresh-signature.mjs";
import { readRefreshTask } from "../src/next/refresh-task.mjs";

const queue = "projects/p/locations/r/queues/q";
const account = "ocel-production-refresh@p.iam.gserviceaccount.com";
const url = "https://web-abc.a.run.app/_ocel/refresh";
const isrPrefix = "prod/shop/web/r1/isr";
const refresh: Refresh = {
  url: "/blog?page=2",
  key: "blog",
  lastModified: 1000,
  headers: { host: "shop.example", "x-ocel-refresh": "1000" },
};

interface Call {
  url: string;
  init: RequestInit;
}

function rig(answers: (call: Call, n: number) => Response | Error) {
  const tasks: Call[] = [];
  const metadata: Call[] = [];
  let tokens = 0;
  const fetchStub = (async (input: string | URL | Request, init: RequestInit = {}) => {
    const call = { url: String(input), init };
    await Promise.resolve();
    if (call.url.includes("computeMetadata")) {
      metadata.push(call);
      return Response.json({ access_token: `t${++tokens}`, expires_in: 3600 });
    }
    tasks.push(call);
    const answer = answers(call, tasks.length);
    if (answer instanceof Error) throw answer;
    return answer;
  }) as typeof fetch;
  return { fetch: fetchStub, tasks, metadata };
}

function schedule(r: ReturnType<typeof rig>, extra: Partial<RefreshQueueOptions> = {}) {
  return newRefreshQueue({
    queue,
    account,
    url,
    isrPrefix,
    secret: "s1",
    fetch: r.fetch,
    random: () => 0,
    sleep: async () => {},
    ...extra,
  });
}

const ok = () => Response.json({});
const hmac = (text: string) => createHmac("sha256", "s1").update(text).digest("hex");
const bodyOf = (call: Call) => JSON.parse(String(call.init.body)).task;
const headersOf = (call: Call) => call.init.headers as Record<string, string>;

test("a stale hit queues a task named for its deployment, entry and generation under its revision's secret", async () => {
  const r = rig(ok);

  await schedule(r)(refresh);

  expect(bodyOf(r.tasks[0]!).name).toBe(`${queue}/tasks/${hmac(`${isrPrefix}\0blog\x001000`)}`);
});

test("a refresh of another generation or another deployment queues a task of another name", async () => {
  const r = rig(ok);

  await schedule(r)(refresh);
  await schedule(r)({ ...refresh, lastModified: 2000 });
  await schedule(r, { isrPrefix: "prod/shop/web/r2/isr" })(refresh);

  const names = r.tasks.map((call) => bodyOf(call).name);
  expect(new Set(names).size).toBe(3);
});

test("the task posts the refresh to the service's refresh url signed as the tier's refresh account", async () => {
  const r = rig(ok);

  await schedule(r)(refresh);

  const call = r.tasks[0]!;
  expect(call.url).toBe(`https://cloudtasks.googleapis.com/v2/${queue}/tasks`);
  expect(call.init.method).toBe("POST");
  expect(headersOf(call)["Content-Type"]).toBe("application/json");
  const task = bodyOf(call);
  expect(task.dispatchDeadline).toBe("60s");
  expect(task.httpRequest).toMatchObject({
    url,
    httpMethod: "POST",
    headers: { "Content-Type": "application/json" },
    oidcToken: { serviceAccountEmail: account, audience: url },
  });
  const payload = Buffer.from(task.httpRequest.body, "base64");
  const decoded = payload.toString();
  expect(JSON.parse(decoded)).toEqual({ isrPrefix, refresh });
  expect(readRefreshTask(decoded)).toEqual({ isrPrefix, refresh });
  const signature = task.httpRequest.headers[refreshSignatureHeader];
  expect(signature).toBe(signRefreshTask("s1", payload));
  expect(isRefreshTaskSignedBy("s1", payload, signature)).toBe(true);
});

test("a revision with a tag url is sent its refresh there, with the service url as the token's audience", async () => {
  const r = rig(ok);
  const target = "https://r0000000a---web-abc.a.run.app/_ocel/refresh";

  await schedule(r, { target })(refresh);

  const task = bodyOf(r.tasks[0]!);
  expect(task.httpRequest.url).toBe(target);
  expect(task.httpRequest.oidcToken.audience).toBe(url);
});

test("a revision with another secret names the same refresh differently", async () => {
  const r = rig(ok);

  await schedule(r)(refresh);
  await schedule(r, { secret: "s2" })(refresh);

  const [first, second] = r.tasks.map((call) => bodyOf(call).name);
  expect(second).not.toBe(first);
});

test("the task is queued as the identity the service runs as", async () => {
  const r = rig(ok);

  await schedule(r)(refresh);

  expect(headersOf(r.tasks[0]!).Authorization).toBe("Bearer t1");
});

test("a refresh already queued counts as queued", async () => {
  const r = rig(() => Response.json({ error: { status: "ALREADY_EXISTS" } }, { status: 409 }));

  await schedule(r)(refresh);

  expect(r.tasks).toHaveLength(1);
});

test("a queue that aborted the call is asked again", async () => {
  const r = rig((_call, n) =>
    n === 1 ? Response.json({ error: { status: "ABORTED" } }, { status: 409 }) : ok(),
  );

  await schedule(r)(refresh);

  expect(r.tasks).toHaveLength(2);
});

test("stale hits of one entry queue one task while the first is in flight", async () => {
  const r = rig(ok);
  const run = schedule(r);

  await Promise.all([run(refresh), run(refresh), run(refresh)]);

  expect(r.tasks).toHaveLength(1);
});

test("an entry queued a moment ago is not sent again", async () => {
  const r = rig(ok);
  let clock = 0;
  const run = schedule(r, { now: () => clock });

  await run(refresh);
  clock = 4 * 60_000;
  await run(refresh);
  expect(r.tasks).toHaveLength(1);

  clock = 5 * 60_000 + 1;
  await run(refresh);
  expect(r.tasks).toHaveLength(2);
});

test("a throttled queue is retried until it takes the task", async () => {
  const answers = [429, 503];
  const r = rig((_call, n) => (n <= 2 ? new Response("", { status: answers[n - 1] }) : ok()));

  await schedule(r)(refresh);

  expect(r.tasks).toHaveLength(3);
});

test("a queue that keeps failing rejects after three attempts naming the page, not the token", async () => {
  const r = rig(() => new Response("", { status: 503 }));

  const failure = await schedule(r)(refresh).catch((error: Error) => error);

  expect(r.tasks).toHaveLength(3);
  expect((failure as Error).message).toMatch(/did not queue the refresh of \/blog\?page=2: 503/);
  expect((failure as Error).message).not.toContain("t1");
});

test("a refusal the queue will repeat is not retried", async () => {
  const r = rig(() => new Response("", { status: 403 }));

  await expect(schedule(r)(refresh)).rejects.toThrow(/403/);
  expect(r.tasks).toHaveLength(1);
});

test("an expired token is fetched again once", async () => {
  const r = rig((_call, n) => (n === 1 ? new Response("", { status: 401 }) : ok()));

  await schedule(r)(refresh);

  expect(r.metadata).toHaveLength(2);
  expect(r.tasks).toHaveLength(2);
  expect(headersOf(r.tasks[1]!).Authorization).toBe("Bearer t2");
});

test("an emulated queue is addressed with no token", async () => {
  const r = rig(ok);

  await schedule(r, { endpoint: "http://127.0.0.1:9000/" })(refresh);

  expect(r.metadata).toHaveLength(0);
  expect(r.tasks[0]!.url).toBe(`http://127.0.0.1:9000/v2/${queue}/tasks`);
  expect(headersOf(r.tasks[0]!).Authorization).toBeUndefined();
});

test("a refresh that cannot be queued names neither the token nor the secret", async () => {
  const r = rig(() => new Response("{}", { status: 503 }));

  const error = await schedule(r)(refresh).catch((e: Error) => e);

  expect(error).toBeInstanceOf(Error);
  expect((error as Error).message).not.toContain("t1");
  expect((error as Error).message).not.toContain("s1");
});

test("a token that never comes fails each attempt after two seconds and names the metadata server", async () => {
  vi.useFakeTimers();
  try {
    const r = rig(() => new Response("{}"));
    const hanging = ((input: string | URL | Request, init?: RequestInit) =>
      String(input).includes("computeMetadata")
        ? new Promise<Response>((_, reject) => {
            init?.signal?.addEventListener("abort", () => reject(new Error("aborted")));
          })
        : r.fetch(input, init)) as typeof fetch;

    const failed = expect(schedule(r, { fetch: hanging })(refresh)).rejects.toThrow(
      /did not queue the refresh of \/blog\?page=2: ocel: the metadata server gave no Cloud Tasks token within 2000 ms/,
    );
    await vi.advanceTimersByTimeAsync(3 * 2_000);

    await failed;
    expect(r.tasks).toHaveLength(0);
  } finally {
    vi.useRealTimers();
  }
});

test("a queue that answers slowly is given up on once eight seconds have passed", async () => {
  vi.useFakeTimers();
  try {
    const tasks: string[] = [];
    const slow = ((input: string | URL | Request, init?: RequestInit) => {
      if (String(input).includes("computeMetadata")) {
        return new Promise<Response>((resolve) => {
          setTimeout(() => resolve(Response.json({ access_token: "t1", expires_in: 0 })), 1_500);
        });
      }
      tasks.push(String(input));
      return new Promise<Response>((_, reject) => {
        init?.signal?.addEventListener("abort", () => reject(new Error("aborted")));
      });
    }) as typeof fetch;
    const started = Date.now();

    const failed = schedule(
      rig(() => new Response("{}")),
      { fetch: slow },
    )(refresh).catch((error: Error) => error);
    await vi.advanceTimersByTimeAsync(8_000);

    expect(((await failed) as Error).message).toMatch(
      /did not queue the refresh of \/blog\?page=2/,
    );
    expect(Date.now() - started).toBeLessThanOrEqual(8_000);
    expect(tasks).toHaveLength(2);
  } finally {
    vi.useRealTimers();
  }
});

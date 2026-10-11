import { createHmac } from "node:crypto";

import { beforeEach, describe, expect, it } from "vitest";

import {
  postPurge,
  purgeSecret,
  purgeSignatureHeader,
  tagsPerPurgeRequest,
} from "../src/purge.mjs";

const URL = "https://acme-prod.sub.workers.dev/.ocel/purge";
const PREFIX = "prod/acme/web/r0a1b2c3d/isr";
const SEED = "seed-1";

interface Sent {
  url: string;
  signature: string | null;
  body: string;
}

class FakeWorker {
  sent: Sent[] = [];
  answers: (Response | Error)[] = [];

  fetch = async (url: string | URL | Request, init?: RequestInit): Promise<Response> => {
    this.sent.push({
      url: String(url),
      signature: new Headers(init?.headers).get(purgeSignatureHeader),
      body: String(init?.body),
    });
    const answer = this.answers.shift() ?? new Response(null, { status: 202 });
    if (answer instanceof Error) throw answer;
    return answer;
  };
}

let worker: FakeWorker;
let waits: number[];

const sender = () => ({
  fetch: worker.fetch as typeof fetch,
  sleep: async (ms: number) => void waits.push(ms),
  random: () => 1,
});

beforeEach(() => {
  worker = new FakeWorker();
  waits = [];
});

describe("purgeSecret", () => {
  it("is the HMAC of the release's ISR prefix under the seed, in hex", () => {
    const expected = createHmac("sha256", SEED).update(PREFIX).digest("hex");

    expect(purgeSecret(SEED, PREFIX)).toBe(expected);
  });

  it("differs per prefix and per seed", () => {
    expect(purgeSecret(SEED, PREFIX)).not.toBe(purgeSecret(SEED, "prod/acme/web/r9/isr"));
    expect(purgeSecret(SEED, PREFIX)).not.toBe(purgeSecret("seed-2", PREFIX));
  });
});

describe("postPurge", () => {
  const secret = purgeSecret(SEED, PREFIX);

  it("posts the release, the mode and each tag with its raisedAt, signed over the body", async () => {
    await postPurge(sender(), URL, secret, "r0a1b2c3d", [
      { tag: "products", mode: "purge", raisedAt: 100 },
    ]);

    expect(worker.sent).toHaveLength(1);
    const sent = worker.sent[0]!;
    expect(sent.url).toBe(URL);
    expect(JSON.parse(sent.body)).toEqual({
      release: "r0a1b2c3d",
      mode: "purge",
      tags: [{ tag: "products", raisedAt: 100 }],
    });
    expect(sent.signature).toBe(createHmac("sha256", secret).update(sent.body).digest("hex"));
  });

  it("sends purges and invalidates as two requests", async () => {
    await postPurge(sender(), URL, secret, "r1", [
      { tag: "a", mode: "invalidate", raisedAt: 1 },
      { tag: "b", mode: "purge", raisedAt: 2 },
    ]);

    expect(worker.sent.map((s) => JSON.parse(s.body).mode).sort()).toEqual(["invalidate", "purge"]);
  });

  it("splits a mode into requests of at most the tags one request may carry", async () => {
    const entries = Array.from({ length: tagsPerPurgeRequest + 1 }, (_, i) => ({
      tag: `t${i}`,
      mode: "purge" as const,
      raisedAt: 1,
    }));

    await postPurge(sender(), URL, secret, "r1", entries);

    expect(worker.sent.map((s) => JSON.parse(s.body).tags.length)).toEqual([
      tagsPerPurgeRequest,
      1,
    ]);
  });

  it("sends nothing for no tags", async () => {
    await postPurge(sender(), URL, secret, "r1", []);

    expect(worker.sent).toEqual([]);
  });

  it("treats 204, the answer for a release no longer served, as sent", async () => {
    worker.answers.push(new Response(null, { status: 204 }));

    await postPurge(sender(), URL, secret, "r1", [{ tag: "a", mode: "purge", raisedAt: 1 }]);

    expect(worker.sent).toHaveLength(1);
  });

  it.each([429, 500, 503])("waits and sends again after a %i", async (status) => {
    worker.answers.push(new Response(null, { status }));

    await postPurge(sender(), URL, secret, "r1", [{ tag: "a", mode: "purge", raisedAt: 1 }]);

    expect(worker.sent).toHaveLength(2);
    expect(worker.sent[1]).toEqual(worker.sent[0]);
    expect(waits).toHaveLength(1);
  });

  it("waits and sends again after the network fails", async () => {
    worker.answers.push(new TypeError("fetch failed"));

    await postPurge(sender(), URL, secret, "r1", [{ tag: "a", mode: "purge", raisedAt: 1 }]);

    expect(worker.sent).toHaveLength(2);
  });

  it("backs off with a growing ceiling and jitter", async () => {
    for (let i = 0; i < 3; i++) worker.answers.push(new Response(null, { status: 503 }));
    const jittered = { ...sender(), random: () => 0.5 };

    await postPurge(jittered, URL, secret, "r1", [{ tag: "a", mode: "purge", raisedAt: 1 }]);

    expect(waits).toHaveLength(3);
    expect(waits[0]).toBeLessThan(waits[1]!);
    expect(waits[1]).toBeLessThan(waits[2]!);
    expect(waits.every((ms) => ms > 0 && ms <= 10_000)).toBe(true);
  });

  it("gives up on a worker that keeps refusing, naming the status and not the secret", async () => {
    for (let i = 0; i < 10; i++) worker.answers.push(new Response(null, { status: 503 }));

    const failure = await postPurge(sender(), URL, secret, "r1", [
      { tag: "a", mode: "purge", raisedAt: 1 },
    ]).catch((error: Error) => error);

    expect(failure).toBeInstanceOf(Error);
    expect((failure as Error).message).toContain("503");
    expect((failure as Error).message).not.toContain(secret);
    expect(worker.sent.length).toBeLessThan(10);
  });

  it.each([400, 401, 404])(
    "does not retry a %i, which sending again cannot fix",
    async (status) => {
      worker.answers.push(new Response(null, { status }));

      await expect(
        postPurge(sender(), URL, secret, "r1", [{ tag: "a", mode: "purge", raisedAt: 1 }]),
      ).rejects.toThrow(String(status));

      expect(worker.sent).toHaveLength(1);
    },
  );
});

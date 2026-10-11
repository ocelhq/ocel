import { createHmac } from "node:crypto";

import { beforeEach, describe, expect, it } from "vitest";

import {
  derivePurgeSecret,
  postPurge,
  purgeSignatureHeader,
  readPurgeEntry,
  readReleaseToken,
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

describe("derivePurgeSecret", () => {
  it("is the HMAC of the release's ISR prefix under the seed, in hex", () => {
    const expected = createHmac("sha256", SEED).update(PREFIX).digest("hex");

    expect(derivePurgeSecret(SEED, PREFIX)).toBe(expected);
  });

  it("differs per prefix and per seed", () => {
    expect(derivePurgeSecret(SEED, PREFIX)).not.toBe(
      derivePurgeSecret(SEED, "prod/acme/web/r9/isr"),
    );
    expect(derivePurgeSecret(SEED, PREFIX)).not.toBe(derivePurgeSecret("seed-2", PREFIX));
  });
});

describe("readReleaseToken", () => {
  it("is the release token segment of a release's ISR prefix", () => {
    expect(readReleaseToken(PREFIX)).toBe("r0a1b2c3d");
  });

  it("is nothing for a prefix that is not a release's ISR prefix", () => {
    expect(readReleaseToken("prod/acme/web/r0a1b2c3d")).toBeUndefined();
    expect(readReleaseToken("prod/acme/web/r0a1b2c3d/assets")).toBeUndefined();
    expect(readReleaseToken("prod/acme//r0a1b2c3d/isr")).toBeUndefined();
    expect(readReleaseToken("")).toBeUndefined();
  });
});

describe("postPurge", () => {
  const secret = derivePurgeSecret(SEED, PREFIX);

  it("posts the release's ISR prefix, the mode and each tag with its raisedAt, signed over the body", async () => {
    await postPurge(sender(), URL, secret, PREFIX, [
      { tag: "products", mode: "purge", raisedAt: 100 },
    ]);

    expect(worker.sent).toHaveLength(1);
    const sent = worker.sent[0]!;
    expect(sent.url).toBe(URL);
    expect(JSON.parse(sent.body)).toEqual({
      isrPrefix: PREFIX,
      mode: "purge",
      tags: [{ tag: "products", raisedAt: 100 }],
    });
    expect(sent.signature).toBe(createHmac("sha256", secret).update(sent.body).digest("hex"));
  });

  it("sends purges and invalidates as two requests", async () => {
    await postPurge(sender(), URL, secret, PREFIX, [
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

    await postPurge(sender(), URL, secret, PREFIX, entries);

    expect(worker.sent.map((s) => JSON.parse(s.body).tags.length)).toEqual([
      tagsPerPurgeRequest,
      1,
    ]);
  });

  it("sends nothing for no tags", async () => {
    await postPurge(sender(), URL, secret, PREFIX, []);

    expect(worker.sent).toEqual([]);
  });

  it("treats 204, the answer for a release no longer served, as sent", async () => {
    worker.answers.push(new Response(null, { status: 204 }));

    await postPurge(sender(), URL, secret, PREFIX, [{ tag: "a", mode: "purge", raisedAt: 1 }]);

    expect(worker.sent).toHaveLength(1);
  });

  it.each([429, 500, 503])("waits and sends again after a %i", async (status) => {
    worker.answers.push(new Response(null, { status }));

    await postPurge(sender(), URL, secret, PREFIX, [{ tag: "a", mode: "purge", raisedAt: 1 }]);

    expect(worker.sent).toHaveLength(2);
    expect(worker.sent[1]).toEqual(worker.sent[0]);
    expect(waits).toHaveLength(1);
  });

  it("waits and sends again after the network fails", async () => {
    worker.answers.push(new TypeError("fetch failed"));

    await postPurge(sender(), URL, secret, PREFIX, [{ tag: "a", mode: "purge", raisedAt: 1 }]);

    expect(worker.sent).toHaveLength(2);
  });

  it("abandons an attempt the worker never answers and sends again", async () => {
    let attempts = 0;
    const stalling = {
      ...sender(),
      attemptTimeoutMs: 5,
      fetch: (async (_url: string | URL | Request, init?: RequestInit) => {
        attempts++;
        if (attempts > 1) return new Response(null, { status: 202 });
        return new Promise<Response>((_, refuse) =>
          init?.signal?.addEventListener("abort", () => refuse(init.signal!.reason)),
        );
      }) as typeof fetch,
    };

    await postPurge(stalling, URL, secret, PREFIX, [{ tag: "a", mode: "purge", raisedAt: 1 }]);

    expect(attempts).toBe(2);
  });

  it("backs off with a growing ceiling and jitter", async () => {
    for (let i = 0; i < 3; i++) worker.answers.push(new Response(null, { status: 503 }));
    const jittered = { ...sender(), random: () => 0.5 };

    await postPurge(jittered, URL, secret, PREFIX, [{ tag: "a", mode: "purge", raisedAt: 1 }]);

    expect(waits).toHaveLength(3);
    expect(waits[0]).toBeLessThan(waits[1]!);
    expect(waits[1]).toBeLessThan(waits[2]!);
    expect(waits.every((ms) => ms > 0 && ms <= 10_000)).toBe(true);
  });

  it("gives up on a worker that keeps refusing, naming the status and not the secret", async () => {
    for (let i = 0; i < 10; i++) worker.answers.push(new Response(null, { status: 503 }));

    const failure = await postPurge(sender(), URL, secret, PREFIX, [
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
        postPurge(sender(), URL, secret, PREFIX, [{ tag: "a", mode: "purge", raisedAt: 1 }]),
      ).rejects.toThrow(String(status));

      expect(worker.sent).toHaveLength(1);
    },
  );
});

describe("readPurgeEntry", () => {
  const now = 10_000;

  it("reads a tag expired when it was raised as a purge raised then", () => {
    expect(readPurgeEntry("cart", { expired: 300 }, now)).toEqual({
      tag: "cart",
      mode: "purge",
      raisedAt: 300,
    });
  });

  it("reads a tag stale now and expiring later as an invalidate raised when it went stale", () => {
    expect(readPurgeEntry("home", { stale: 200, expired: 90_000 }, now)).toEqual({
      tag: "home",
      mode: "invalidate",
      raisedAt: 200,
    });
  });

  it("reads a tag only marked stale as an invalidate", () => {
    expect(readPurgeEntry("home", { stale: 200 }, now)).toEqual({
      tag: "home",
      mode: "invalidate",
      raisedAt: 200,
    });
  });

  it("reads a tag stale and expired at once as a purge", () => {
    expect(readPurgeEntry("home", { stale: 200, expired: 200 }, now)).toEqual({
      tag: "home",
      mode: "purge",
      raisedAt: 200,
    });
  });

  it("reads a tag gone stale and later expired as a purge raised when it expired", () => {
    expect(readPurgeEntry("home", { stale: 200, expired: 2_000 }, now)).toEqual({
      tag: "home",
      mode: "purge",
      raisedAt: 2_000,
    });
  });

  it("reads a tag expired and later gone stale as an invalidate raised when it went stale", () => {
    expect(readPurgeEntry("home", { stale: 2_000, expired: 200 }, now)).toEqual({
      tag: "home",
      mode: "invalidate",
      raisedAt: 2_000,
    });
  });

  it("reads a record with neither time as nothing to purge", () => {
    expect(readPurgeEntry("home", {}, now)).toBeUndefined();
  });
});

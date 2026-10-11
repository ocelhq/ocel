import {
  type PurgeMode,
  purgePath,
  purgeSignatureHeader,
  tagsPerPurgeRequest,
} from "@platform/edge-contract/purge";
import { beforeEach, describe, expect, it } from "vitest";
import type { RaisedTag } from "../src/purge-backlog";
import {
  answerPurge,
  isPurgeRequest,
  maxPurgeBodyBytes,
  type PurgeGatewayPorts,
} from "../src/purge-request";

const SECRET = "ab12cd34".repeat(8);

async function sign(secret: string, body: string): Promise<string> {
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const mac = await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(body));
  return [...new Uint8Array(mac)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

async function signed(
  body: unknown,
  { secret = SECRET, headers = {} as Record<string, string> } = {},
): Promise<Request> {
  const text = typeof body === "string" ? body : JSON.stringify(body);
  return new Request(`https://p.acct.workers.dev${purgePath}`, {
    method: "POST",
    headers: { [purgeSignatureHeader]: await sign(secret, text), ...headers },
    body: text,
  });
}

const raise = { release: "r1", mode: "purge", tags: [{ tag: "_N_T_/blog/a", raisedAt: 1_000 }] };

let raised: { mode: PurgeMode; tags: RaisedTag[] }[];
let served: Record<string, { purgeSecret?: string }>;
let storeDown: boolean;
let warnings: string[];

function deps(): PurgeGatewayPorts {
  return {
    now: () => 1_000_000,
    async readServed(release) {
      if (storeDown) throw new Error("releases store is down");
      const record = served[release];
      return record ? { kind: "record", record } : { kind: "not-served" };
    },
    queue: {
      async raise(mode, tags) {
        raised.push({ mode, tags });
      },
    },
    warn: (message) => warnings.push(message),
  };
}

beforeEach(() => {
  raised = [];
  served = { r1: { purgeSecret: SECRET } };
  storeDown = false;
  warnings = [];
});

describe("recognising a purge request", () => {
  it("is a POST to the purge path", () => {
    expect(
      isPurgeRequest(new Request(`https://x.workers.dev${purgePath}`, { method: "POST" })),
    ).toBe(true);
  });

  it("is not a GET of the purge path, which the app may serve", () => {
    expect(isPurgeRequest(new Request(`https://x.workers.dev${purgePath}`))).toBe(false);
  });

  it("is not a POST to any other path", () => {
    expect(isPurgeRequest(new Request("https://x.workers.dev/api/purge", { method: "POST" }))).toBe(
      false,
    );
  });
});

describe("answering a signed purge request", () => {
  it("hands the queue the release-stamped tags with the mode, and accepts", async () => {
    const response = await answerPurge(await signed(raise), deps());

    expect(response.status).toBe(202);
    expect(raised).toEqual([
      { mode: "purge", tags: [{ tag: "r1|_N_T_/blog/a", raisedAt: 1_000 }] },
    ]);
  });

  it("passes an invalidate through as an invalidate", async () => {
    await answerPurge(await signed({ ...raise, mode: "invalidate" }), deps());

    expect(raised[0].mode).toBe("invalidate");
  });

  it("keeps each tag's own raisedAt", async () => {
    const tags = [
      { tag: "a", raisedAt: 10 },
      { tag: "b", raisedAt: 20 },
    ];

    await answerPurge(await signed({ ...raise, tags }), deps());

    expect(raised[0].tags).toEqual([
      { tag: "r1|a", raisedAt: 10 },
      { tag: "r1|b", raisedAt: 20 },
    ]);
  });

  it("dates a raise from the future no later than the worker's own clock", async () => {
    const tags = [
      { tag: "early", raisedAt: 999_000 },
      { tag: "ahead", raisedAt: 5_000_000 },
    ];

    await answerPurge(await signed({ ...raise, tags }), deps());

    expect(raised[0].tags).toEqual([
      { tag: "r1|early", raisedAt: 999_000 },
      { tag: "r1|ahead", raisedAt: 1_000_000 },
    ]);
  });

  it("checks the signature over the bytes sent, not their decoding", async () => {
    const bytes = new Uint8Array([
      0xef,
      0xbb,
      0xbf,
      ...new TextEncoder().encode(JSON.stringify(raise)),
    ]);
    const key = await crypto.subtle.importKey(
      "raw",
      new TextEncoder().encode(SECRET),
      { name: "HMAC", hash: "SHA-256" },
      false,
      ["sign"],
    );
    const mac = new Uint8Array(await crypto.subtle.sign("HMAC", key, bytes));
    const signature = [...mac].map((byte) => byte.toString(16).padStart(2, "0")).join("");

    const response = await answerPurge(
      new Request(`https://p.acct.workers.dev${purgePath}`, {
        method: "POST",
        headers: { [purgeSignatureHeader]: signature },
        body: bytes,
      }),
      deps(),
    );

    expect(response.status).toBe(202);
  });

  it("answers 413 from a content-length over the cap without reading the body", async () => {
    let pulled = false;
    const body = new ReadableStream<Uint8Array>(
      {
        pull(controller) {
          pulled = true;
          controller.enqueue(new TextEncoder().encode(JSON.stringify(raise)));
          controller.close();
        },
      },
      { highWaterMark: 0 },
    );
    const request = new Request(`https://p.acct.workers.dev${purgePath}`, {
      method: "POST",
      headers: { "content-length": String(maxPurgeBodyBytes + 1) },
      body,
    });

    const response = await answerPurge(request, deps());

    expect(response.status).toBe(413);
    expect(pulled).toBe(false);
  });

  it("answers 413 and stops reading a body with no content-length once it passes the cap", async () => {
    const chunk = new Uint8Array(64 * 1024).fill(0x20);
    const body = new ReadableStream<Uint8Array>({
      pull(controller) {
        controller.enqueue(chunk);
      },
    });
    const request = new Request(`https://p.acct.workers.dev${purgePath}`, {
      method: "POST",
      body,
    });

    const response = await answerPurge(request, deps());

    expect(response.status).toBe(413);
  });

  it("accepts an upper-case signature", async () => {
    const request = await signed(raise);
    const upper = request.headers.get(purgeSignatureHeader)!.toUpperCase();
    const text = JSON.stringify(raise);

    const response = await answerPurge(
      new Request(request.url, {
        method: "POST",
        headers: { [purgeSignatureHeader]: upper },
        body: text,
      }),
      deps(),
    );

    expect(response.status).toBe(202);
  });

  it("drops a tag too long to be stored with its release, and warns", async () => {
    const response = await answerPurge(
      await signed({ ...raise, tags: [{ tag: "x".repeat(300), raisedAt: 1 }, raise.tags[0]] }),
      deps(),
    );

    expect(response.status).toBe(202);
    expect(raised[0].tags.map((t) => t.tag)).toEqual(["r1|_N_T_/blog/a"]);
    expect(warnings).toHaveLength(1);
  });

  it("raises nothing when every tag is unstorable", async () => {
    const response = await answerPurge(
      await signed({ ...raise, tags: [{ tag: "x".repeat(300), raisedAt: 1 }] }),
      deps(),
    );

    expect(response.status).toBe(202);
    expect(raised).toEqual([]);
  });
});

describe("refusing a purge request", () => {
  it("answers 204 for a release that is not served, since nothing caches under it", async () => {
    const response = await answerPurge(await signed({ ...raise, release: "gone" }), deps());

    expect(response.status).toBe(204);
    expect(raised).toEqual([]);
  });

  it("answers 401 for a request with no signature", async () => {
    const response = await answerPurge(
      new Request("https://x.workers.dev" + purgePath, {
        method: "POST",
        body: JSON.stringify(raise),
      }),
      deps(),
    );

    expect(response.status).toBe(401);
    expect(raised).toEqual([]);
  });

  it("answers 401 for a signature made with another release's secret", async () => {
    const response = await answerPurge(await signed(raise, { secret: "cd".repeat(32) }), deps());

    expect(response.status).toBe(401);
    expect(raised).toEqual([]);
  });

  it("answers 401 for a signature of a different body", async () => {
    const forged = await signed({ ...raise, tags: [{ tag: "other", raisedAt: 1 }] });
    const request = new Request(forged.url, {
      method: "POST",
      headers: forged.headers,
      body: JSON.stringify(raise),
    });

    const response = await answerPurge(request, deps());

    expect(response.status).toBe(401);
  });

  it("answers 401 for a signature that is not hex", async () => {
    const response = await answerPurge(
      await signed(raise, { headers: { [purgeSignatureHeader]: "not-hex!" } }),
      deps(),
    );

    expect(response.status).toBe(401);
  });

  it("answers 401 when the served release has no purge secret to check against", async () => {
    served.r1 = {};

    const response = await answerPurge(await signed(raise), deps());

    expect(response.status).toBe(401);
    expect(raised).toEqual([]);
  });

  it("answers 400 for a body that is not JSON", async () => {
    const response = await answerPurge(await signed("not json"), deps());

    expect(response.status).toBe(400);
  });

  it.each([
    ["no release", { mode: "purge", tags: [] }],
    ["an unknown mode", { release: "r1", mode: "obliterate", tags: [] }],
    ["tags that are not a list", { release: "r1", mode: "purge", tags: "a" }],
    ["a tag with no raisedAt", { release: "r1", mode: "purge", tags: [{ tag: "a" }] }],
    ["an empty tag", { release: "r1", mode: "purge", tags: [{ tag: "", raisedAt: 1 }] }],
    [
      "a raisedAt that is not finite",
      { release: "r1", mode: "purge", tags: [{ tag: "a", raisedAt: null }] },
    ],
  ])("answers 400 for %s", async (_name, body) => {
    const response = await answerPurge(await signed(body), deps());

    expect(response.status).toBe(400);
    expect(raised).toEqual([]);
  });

  it("answers 413 for more tags than one request may carry", async () => {
    const tags = Array.from({ length: tagsPerPurgeRequest + 1 }, (_, i) => ({
      tag: `t${i}`,
      raisedAt: 1,
    }));

    const response = await answerPurge(await signed({ ...raise, tags }), deps());

    expect(response.status).toBe(413);
  });

  it("answers 503 when the releases store cannot say whether the release is served", async () => {
    storeDown = true;

    const response = await answerPurge(await signed(raise), deps());

    expect(response.status).toBe(503);
    expect(raised).toEqual([]);
  });

  it("answers 503 and raises nothing when the queue refuses the tags", async () => {
    const refusing = deps();
    refusing.queue.raise = async () => {
      throw new Error("queue is down");
    };

    const response = await answerPurge(await signed(raise), refusing);

    expect(response.status).toBe(503);
  });
});

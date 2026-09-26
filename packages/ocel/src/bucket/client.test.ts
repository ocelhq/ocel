import { describe, expect, it, vi } from "vitest";
import type { Bucket } from "./bucket.js";
import { createUploadClient } from "./client.js";
import type { Uploader } from "./types.js";

type TestBucket = Bucket<{ avatar: Uploader<{ userId: string }, unknown> }>;

function jsonRes(body: unknown, ok = true, status = 200) {
  return { ok, status, json: async () => body };
}

interface FetchInit {
  method?: string;
  body?: unknown;
  headers?: Record<string, string>;
}

function fakeFetch(pollStates: string[]) {
  let poll = 0;
  return vi.fn(async (url: string, _init?: FetchInit) => {
    if (url.includes("op=presign")) {
      return jsonRes({
        sessionId: "sess-1",
        files: [{ url: "https://store/put/a", key: "avatars/a.jpg", name: "a.jpg" }],
      });
    }
    if (url.includes("op=poll")) {
      const state = pollStates[Math.min(poll++, pollStates.length - 1)];
      return jsonRes({ state });
    }
    return jsonRes({});
  });
}

const file = { name: "a.jpg", size: 10, type: "image/jpeg" };

describe("createUploadClient", () => {
  it("presigns, PUTs bytes directly, polls to succeeded, and fires onClientUploadComplete", async () => {
    const fetch = fakeFetch(["pending", "succeeded"]);
    const client = createUploadClient<TestBucket>({
      url: "https://app/api/upload",
      pollIntervalMs: 1,
      fetch,
    });
    const onClientUploadComplete = vi.fn();

    const result = await client.upload(
      "avatar",
      { files: [file], input: { userId: "u1" } },
      { onClientUploadComplete },
    );

    const presignCall = fetch.mock.calls.find((c) => c[0].includes("op=presign"))!;
    expect(JSON.parse(presignCall[1]!.body as string)).toEqual({
      uploader: "avatar",
      input: { userId: "u1" },
      files: [{ name: "a.jpg", size: 10, mimeType: "image/jpeg" }],
    });

    const putCall = fetch.mock.calls.find((c) => c[0] === "https://store/put/a")!;
    expect(putCall[1]!.method).toBe("PUT");
    expect(putCall[1]!.body).toBe(file);

    expect(onClientUploadComplete).toHaveBeenCalledWith({
      files: [{ key: "avatars/a.jpg", name: "a.jpg" }],
    });
    expect(result.files).toEqual([{ key: "avatars/a.jpg", name: "a.jpg" }]);
  });

  it("sends the headers a target states, beside its content disposition", async () => {
    const fetch = vi.fn(async (url: string, _init?: FetchInit) => {
      if (url.includes("op=presign")) {
        return jsonRes({
          sessionId: "sess-1",
          files: [
            {
              url: "https://store/put/a",
              key: "avatars/a.jpg",
              name: "a.jpg",
              contentDisposition: "inline",
              headers: { "x-amz-tagging": "sessionId=sess-1" },
            },
          ],
        });
      }
      return jsonRes({ state: "succeeded" });
    });
    const client = createUploadClient<TestBucket>({
      url: "https://app/api/upload",
      pollIntervalMs: 1,
      fetch,
    });

    await client.upload("avatar", { files: [file], input: { userId: "u1" } });

    const putCall = fetch.mock.calls.find((c) => c[0] === "https://store/put/a")!;
    expect(putCall[1]!.headers).toEqual({
      "x-amz-tagging": "sessionId=sess-1",
      "content-disposition": "inline",
    });
  });

  it("re-polls until a terminal state", async () => {
    const fetch = fakeFetch(["pending", "pending", "succeeded"]);
    const client = createUploadClient<TestBucket>({
      url: "https://app/api/upload",
      pollIntervalMs: 1,
      fetch,
    });
    await client.upload("avatar", { files: [file], input: { userId: "u1" } });
    const pollCalls = fetch.mock.calls.filter((c) => c[0].includes("op=poll"));
    expect(pollCalls.length).toBe(3);
  });

  it("waits longer between polls the longer an upload stays pending", async () => {
    const fetch = fakeFetch(["pending", "pending", "pending", "pending", "pending", "succeeded"]);
    const delays: number[] = [];
    const realSetTimeout = globalThis.setTimeout;
    const spied = vi.spyOn(globalThis, "setTimeout").mockImplementation(((
      callback: () => void,
      ms?: number,
    ) => {
      delays.push(ms ?? 0);
      return realSetTimeout(callback, 0);
    }) as never);
    const client = createUploadClient<TestBucket>({
      url: "https://app/api/upload",
      pollIntervalMs: 100,
      fetch,
    });

    await client.upload("avatar", { files: [file], input: { userId: "u1" } });
    spied.mockRestore();

    expect(delays.length).toBe(5);
    expect(delays).toEqual([...delays].sort((a, b) => a - b));
    expect(new Set(delays).size).toBeGreaterThan(1);
    for (const delay of delays) expect(delay).toBeLessThanOrEqual(15_000);
  });

  it("throws immediately when a presigned PUT returns non-2xx (no polling)", async () => {
    const fetch = vi.fn(async (url: string) => {
      if (url.includes("op=presign")) {
        return jsonRes({
          sessionId: "sess-1",
          files: [{ url: "https://store/put/a", key: "avatars/a.jpg", name: "a.jpg" }],
        });
      }
      if (url === "https://store/put/a") return jsonRes({}, false, 403);
      return jsonRes({});
    });
    const client = createUploadClient<TestBucket>({
      url: "https://app/api/upload",
      pollIntervalMs: 1,
      fetch,
    });
    await expect(
      client.upload("avatar", { files: [file], input: { userId: "u1" } }),
    ).rejects.toThrow("upload PUT failed (403)");
    expect(fetch.mock.calls.some((c) => c[0].includes("op=poll"))).toBe(false);
  });

  it("calls onError and throws when the upload expires", async () => {
    const fetch = fakeFetch(["expired"]);
    const client = createUploadClient<TestBucket>({
      url: "https://app/api/upload",
      pollIntervalMs: 1,
      fetch,
    });
    const onError = vi.fn();
    await expect(
      client.upload("avatar", { files: [file], input: { userId: "u1" } }, { onError }),
    ).rejects.toThrow("upload expired");
    expect(onError).toHaveBeenCalled();
  });

  it("tells the route the upload is finished before it starts polling", async () => {
    const fetch = fakeFetch(["succeeded"]);
    const client = createUploadClient<TestBucket>({
      url: "https://app/api/upload",
      pollIntervalMs: 1,
      fetch,
    });

    await client.upload("avatar", { files: [file], input: { userId: "u1" } });

    const order = fetch.mock.calls.map((c) => c[0]);
    const completed = order.findIndex((url) => url.includes("op=complete"));
    const polled = order.findIndex((url) => url.includes("op=poll"));
    expect(completed).toBeGreaterThan(order.indexOf("https://store/put/a"));
    expect(polled).toBeGreaterThan(completed);
    expect(JSON.parse(fetch.mock.calls[completed]![1]!.body as string)).toEqual({
      sessionId: "sess-1",
    });
  });

  it("posts a form when the target includes policy fields", async () => {
    const fetch = vi.fn(async (url: string, _init?: FetchInit) => {
      if (url.includes("op=presign")) {
        return jsonRes({
          sessionId: "sess-1",
          files: [
            {
              url: "https://store/post",
              key: "avatars/a.jpg",
              name: "a.jpg",
              method: "POST",
              fields: { key: "avatars/a.jpg", policy: "signed" },
            },
          ],
        });
      }
      if (url.includes("op=poll")) return jsonRes({ state: "succeeded" });
      return jsonRes({});
    });
    const client = createUploadClient<TestBucket>({
      url: "https://app/api/upload",
      pollIntervalMs: 1,
      fetch,
    });

    await client.upload("avatar", { files: [file], input: { userId: "u1" } });

    const post = fetch.mock.calls.find((c) => c[0] === "https://store/post")!;
    expect(post[1]!.method).toBe("POST");
    const form = post[1]!.body as FormData;
    expect(form).toBeInstanceOf(FormData);
    expect(form.get("policy")).toBe("signed");
    expect(form.get("key")).toBe("avatars/a.jpg");
    expect(post[1]!.headers).toBeUndefined();
  });
});

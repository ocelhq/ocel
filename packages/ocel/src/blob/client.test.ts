import { describe, expect, it, vi } from "vitest";
import { createUploadClient } from "./client";
import type { Bucket } from "./bucket";
import type { Uploader } from "./types";

// A structural bucket type is enough to exercise the runtime client; the fake
// fetch stands in for the route.
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
    // PUT to the presigned target
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

    // presign body carries uploader name + client-reported file identity
    const presignCall = fetch.mock.calls.find((c) => c[0].includes("op=presign"))!;
    expect(JSON.parse(presignCall[1]!.body as string)).toEqual({
      uploader: "avatar",
      input: { userId: "u1" },
      files: [{ name: "a.jpg", size: 10, mimeType: "image/jpeg" }],
    });

    // bytes go directly to the presigned target via PUT (not through the route)
    const putCall = fetch.mock.calls.find((c) => c[0] === "https://store/put/a")!;
    expect(putCall[1]!.method).toBe("PUT");
    expect(putCall[1]!.body).toBe(file);

    expect(onClientUploadComplete).toHaveBeenCalledWith({
      files: [{ key: "avatars/a.jpg", name: "a.jpg" }],
    });
    expect(result.files).toEqual([{ key: "avatars/a.jpg", name: "a.jpg" }]);
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
});

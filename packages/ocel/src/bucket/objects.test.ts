import { describe, expect, it, vi } from "vitest";
import { ObjectNotFoundError, PreconditionFailedError } from "./errors.js";
import { createObjects } from "./objects.js";

interface StoredObject {
  body: Uint8Array;
  contentType: string;
  etag: string;
}

function fakeRuntime() {
  const store = new Map<string, StoredObject>();
  const multiparts = new Map<string, Uint8Array[]>();
  const signed = new Map<string, { key: string; kind: "get" | "put"; part?: number }>();
  let nextUrl = 0;

  const url = (key: string, kind: "get" | "put", part?: number) => {
    const at = `https://store.test/signed/${nextUrl++}`;
    signed.set(at, { key, kind, part });
    return at;
  };

  const info = (key: string) => {
    const stored = store.get(key);
    if (!stored) return undefined;
    return {
      key,
      size: BigInt(stored.body.byteLength),
      etag: stored.etag,
      contentType: stored.contentType,
      uploadedAt: undefined,
      metadata: {},
    };
  };

  const client = {
    head: vi.fn(async ({ key }: { key: string }) => ({ object: info(key) })),
    list: vi.fn(
      async ({ prefix, limit, cursor }: { prefix: string; limit: number; cursor: string }) => {
        const keys = [...store.keys()].filter((k) => k.startsWith(prefix ?? "")).sort();
        const start = cursor ? keys.indexOf(cursor) + 1 : 0;
        const page = keys.slice(start, start + (limit || 1000));
        const last = page.at(-1);
        return {
          objects: page.map((k) => info(k)),
          nextCursor: last && keys.indexOf(last) < keys.length - 1 ? last : "",
        };
      },
    ),
    delete: vi.fn(async ({ keys }: { keys: string[] }) => {
      for (const key of keys) store.delete(key);
      return {};
    }),
    copy: vi.fn(
      async ({ sourceKey, destinationKey }: { sourceKey: string; destinationKey: string }) => {
        const stored = store.get(sourceKey);
        if (!stored) throw Object.assign(new Error("not found"), { code: 5 });
        store.set(destinationKey, stored);
        return { object: info(destinationKey) };
      },
    ),
    sign: vi.fn(async ({ key, operation }: { key: string; operation: number }) => ({
      target: {
        url: url(key, operation === 1 ? "get" : "put"),
        key,
        method: operation === 1 ? "GET" : "PUT",
        headers: {},
        fields: {},
      },
    })),
    createMultipart: vi.fn(async ({ key }: { key: string }) => {
      const uploadId = `mpu-${key}`;
      multiparts.set(uploadId, []);
      return { uploadId };
    }),
    signParts: vi.fn(async ({ key, partNumbers }: { key: string; partNumbers: number[] }) => ({
      parts: partNumbers.map((partNumber) => ({
        partNumber,
        url: url(key, "put", partNumber),
        headers: {},
      })),
    })),
    completeMultipart: vi.fn(async ({ key, uploadId }: { key: string; uploadId: string }) => {
      const parts = multiparts.get(uploadId) ?? [];
      const total = parts.reduce((n, p) => n + p.byteLength, 0);
      const body = new Uint8Array(total);
      let at = 0;
      for (const part of parts) {
        body.set(part, at);
        at += part.byteLength;
      }
      store.set(key, { body, contentType: "application/octet-stream", etag: `"${key}"` });
      multiparts.delete(uploadId);
      return { object: info(key) };
    }),
    abortMultipart: vi.fn(async ({ uploadId }: { uploadId: string }) => {
      multiparts.delete(uploadId);
      return {};
    }),
  };

  const fetchImpl = vi.fn(async (input: string, init?: { method?: string; body?: unknown }) => {
    const target = signed.get(String(input));
    if (!target) return new Response("no such signed url", { status: 403 });
    if (target.kind === "put") {
      const bytes = new Uint8Array(await new Response(init?.body as never).arrayBuffer());
      if (target.part !== undefined) {
        const parts = multiparts.get(`mpu-${target.key}`);
        if (!parts) return new Response("gone", { status: 404 });
        parts[target.part - 1] = bytes;
        return new Response(null, { status: 200, headers: { etag: `"p${target.part}"` } });
      }
      store.set(target.key, {
        body: bytes,
        contentType: "application/octet-stream",
        etag: `"${target.key}"`,
      });
      return new Response(null, { status: 200, headers: { etag: `"${target.key}"` } });
    }
    const stored = store.get(target.key);
    if (!stored) return new Response(null, { status: 404 });
    return new Response(stored.body, { status: 200 });
  });

  return { store, client, fetchImpl, signed };
}

function objectsUnderTest(overrides: { publicBaseUrl?: string } = {}) {
  const runtime = fakeRuntime();
  const objects = createObjects({
    runtime: () => ({
      client: runtime.client as never,
      bucket: "store",
      publicBaseUrl: overrides.publicBaseUrl ?? "",
    }),
    fetch: runtime.fetchImpl as never,
  });
  return { ...runtime, objects };
}

describe("put", () => {
  it("writes a small body in one signed request and returns the object", async () => {
    const { objects, store, client } = objectsUnderTest();

    const info = await objects.put("a.txt", "hello");

    expect(new TextDecoder().decode(store.get("a.txt")?.body)).toBe("hello");
    expect(info.key).toBe("a.txt");
    expect(info.size).toBe(5);
    expect(client.createMultipart).not.toHaveBeenCalled();
  });

  it("splits a body past the single-request ceiling into parts", async () => {
    const { objects, store, client } = objectsUnderTest();
    const big = new Uint8Array(20 * 1024 * 1024).fill(7);

    const info = await objects.put("big.bin", big);

    expect(client.createMultipart).toHaveBeenCalledOnce();
    expect(client.signParts).toHaveBeenCalled();
    expect(store.get("big.bin")?.body.byteLength).toBe(big.byteLength);
    expect(info.size).toBe(big.byteLength);
  });

  it("abandons a multipart write whose part fails, so no parts are left paid for", async () => {
    const { objects, client, fetchImpl } = objectsUnderTest();
    fetchImpl.mockImplementationOnce(async () => new Response("nope", { status: 500 }));

    await expect(objects.put("big.bin", new Uint8Array(20 * 1024 * 1024))).rejects.toThrow();

    expect(client.abortMultipart).toHaveBeenCalledOnce();
  });

  it("cancels the source and the sibling parts when a part fails", async () => {
    const { objects, fetchImpl } = objectsUnderTest();
    let cancelled = false;
    let aborted = 0;
    const source = new ReadableStream<Uint8Array>({
      pull(controller) {
        controller.enqueue(new Uint8Array(4 * 1024 * 1024).fill(3));
      },
      cancel() {
        cancelled = true;
      },
    });
    fetchImpl.mockImplementation((async (_input: string, init?: { signal?: AbortSignal }) => {
      init?.signal?.addEventListener("abort", () => {
        aborted += 1;
      });
      return new Response("nope", { status: 500 });
    }) as never);

    await expect(objects.put("big.bin", source)).rejects.toThrow();

    expect(cancelled).toBe(true);
    expect(aborted).toBeGreaterThan(0);
  });

  it("turns a refused precondition into a typed error", async () => {
    const { objects, fetchImpl } = objectsUnderTest();
    fetchImpl.mockImplementationOnce(async () => new Response("exists", { status: 412 }));

    await expect(objects.put("a.txt", "hello", { ifNoneMatch: "*" })).rejects.toBeInstanceOf(
      PreconditionFailedError,
    );
  });
});

describe("get", () => {
  it("reads the bytes back as text, json and bytes", async () => {
    const { objects } = objectsUnderTest();
    await objects.put("a.json", JSON.stringify({ ok: true }));

    const read = await objects.get("a.json");

    expect(read).not.toBeNull();
    expect(await read?.text()).toBe('{"ok":true}');
    expect(await objects.get("a.json").then((o) => o?.json())).toEqual({ ok: true });
    expect(await objects.get("a.json").then((o) => o?.bytes())).toBeInstanceOf(Uint8Array);
  });

  it("is null for an object that is not there", async () => {
    const { objects } = objectsUnderTest();

    expect(await objects.get("gone.txt")).toBeNull();
  });
});

describe("head", () => {
  it("is the object's info, or null", async () => {
    const { objects } = objectsUnderTest();
    await objects.put("a.txt", "hello");

    expect((await objects.head("a.txt"))?.size).toBe(5);
    expect(await objects.head("gone.txt")).toBeNull();
  });
});

describe("delete", () => {
  it("takes one key or many, and says nothing about the ones that were not there", async () => {
    const { objects, store } = objectsUnderTest();
    await objects.put("a.txt", "a");
    await objects.put("b.txt", "b");

    await objects.delete("a.txt");
    await objects.delete(["b.txt", "never-there.txt"]);

    expect(store.size).toBe(0);
  });
});

describe("list", () => {
  it("walks every page as one async iterable", async () => {
    const { objects } = objectsUnderTest();
    for (const key of ["p/a", "p/b", "p/c", "q/d"]) await objects.put(key, key);

    const seen: string[] = [];
    for await (const info of objects.list({ prefix: "p/", limit: 2 })) seen.push(info.key);

    expect(seen).toEqual(["p/a", "p/b", "p/c"]);
  });

  it("hands back one page and its cursor when asked", async () => {
    const { objects } = objectsUnderTest();
    for (const key of ["a", "b", "c"]) await objects.put(key, key);

    const page = await objects.list({ limit: 2 }).page();

    expect(page.objects.map((o) => o.key)).toEqual(["a", "b"]);
    expect(page.cursor).toBe("b");
  });
});

describe("copy", () => {
  it("copies an object, and refuses a source that is not there", async () => {
    const { objects } = objectsUnderTest();
    await objects.put("a.txt", "hello");

    await objects.copy("a.txt", "b.txt");

    expect(await objects.get("b.txt").then((o) => o?.text())).toBe("hello");
    await expect(objects.copy("gone.txt", "c.txt")).rejects.toBeInstanceOf(ObjectNotFoundError);
  });
});

describe("signedUrl and signedUpload", () => {
  it("hand back urls the caller drives itself", async () => {
    const { objects } = objectsUnderTest();

    const read = await objects.signedUrl("a.txt", { expiresIn: 600 });
    const write = await objects.signedUpload("a.txt", { maxSize: 1024 });

    expect(read).toMatch(/^https:\/\/store.test\/signed\//);
    expect(write.method).toBe("PUT");
    expect(write.url).toMatch(/^https:\/\/store.test\/signed\//);
  });
});

describe("publicUrl", () => {
  it("is the object's address under the bucket's public base", () => {
    const { objects } = objectsUnderTest({ publicBaseUrl: "https://storage.example.com/store" });

    expect(objects.publicUrl("a/b.png")).toBe("https://storage.example.com/store/a/b.png");
  });

  it("escapes what a key segment may contain without leaving the path", () => {
    const { objects } = objectsUnderTest({ publicBaseUrl: "https://storage.example.com/store" });

    expect(objects.publicUrl("a b/c#d?e.png")).toBe(
      "https://storage.example.com/store/a%20b/c%23d%3Fe.png",
    );
  });

  it("refuses when nothing published the bucket", () => {
    const { objects } = objectsUnderTest();

    expect(() => objects.publicUrl("a.png")).toThrow(/public/);
  });
});

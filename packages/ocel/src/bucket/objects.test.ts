import { describe, expect, it, vi } from "vitest";
import { RuntimeCallError } from "../runtime/errors.js";
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
  const etagHeader = "ETag";
  const races = { afterWrite: false, beforeRead: 0, etagless: false };
  let nextUrl = 0;
  let nextVersion = 0;

  const url = (key: string, kind: "get" | "put", part?: number) => {
    const at = `https://store.test/signed/${nextUrl++}`;
    signed.set(at, { key, kind, part });
    return at;
  };

  const write = (key: string, body: Uint8Array, contentType: string) => {
    const etag = `"v${++nextVersion}"`;
    store.set(key, { body, contentType, etag });
    return etag;
  };

  const anotherWriterLands = (key: string) =>
    write(key, new TextEncoder().encode("another writer's bytes"), "text/plain");

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
        if (!stored) {
          throw new RuntimeCallError("app.bucket.v1.BucketService/Copy", "not_found", "gone", null);
        }
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
        etagHeader: operation === 1 || operation === 2 ? etagHeader : "",
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
      const etag = write(key, body, "application/octet-stream");
      multiparts.delete(uploadId);
      if (races.afterWrite) anotherWriterLands(key);
      return { etag: races.etagless ? "" : etag };
    }),
    abortMultipart: vi.fn(async ({ uploadId }: { uploadId: string }) => {
      multiparts.delete(uploadId);
      return {};
    }),
  };

  const fetchImpl = vi.fn(
    async (
      input: string,
      init?: { method?: string; body?: unknown; headers?: Record<string, string> },
    ) => {
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
        const etag = write(target.key, bytes, init?.headers?.["content-type"] ?? "");
        if (races.afterWrite) anotherWriterLands(target.key);
        return new Response(null, {
          status: 200,
          headers: races.etagless ? {} : { [etagHeader]: etag },
        });
      }
      if (races.beforeRead > 0) {
        races.beforeRead -= 1;
        anotherWriterLands(target.key);
      }
      const stored = store.get(target.key);
      if (!stored) return new Response(null, { status: 404 });
      const range = /^bytes=(\d+)-(\d*)$/.exec(init?.headers?.range ?? "");
      const body = range
        ? stored.body.subarray(Number(range[1]), range[2] ? Number(range[2]) + 1 : undefined)
        : stored.body;
      return new Response(body, {
        status: range ? 206 : 200,
        headers: { [etagHeader]: stored.etag },
      });
    },
  );

  return { store, client, fetchImpl, signed, races };
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

  it("writes an empty stream as an empty object in one signed request", async () => {
    const { objects, store, client } = objectsUnderTest();
    client.completeMultipart.mockImplementation((async ({ parts }: { parts: unknown[] }) => {
      if (parts.length === 0) throw Object.assign(new Error("parts: min_items 1"), { code: 3 });
      return { etag: "" };
    }) as never);

    const info = await objects.put(
      "empty.bin",
      new ReadableStream<Uint8Array>({ start: (c) => c.close() }),
    );

    expect(store.get("empty.bin")?.body.byteLength).toBe(0);
    expect(info.size).toBe(0);
    expect(client.createMultipart).not.toHaveBeenCalled();
  });

  it("writes a stream shorter than one part in one signed request", async () => {
    const { objects, store, client } = objectsUnderTest();
    const source = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new TextEncoder().encode("hel"));
        controller.enqueue(new TextEncoder().encode("lo"));
        controller.close();
      },
    });

    await objects.put("small.txt", source);

    expect(new TextDecoder().decode(store.get("small.txt")?.body)).toBe("hello");
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

  it("returns the etag of its own write, though another write replaces the object at once", async () => {
    const { objects, store, races } = objectsUnderTest();
    races.afterWrite = true;

    const info = await objects.put("a.txt", "hello");

    expect(info.etag).toBe('"v1"');
    expect(store.get("a.txt")?.etag).toBe('"v2"');
  });

  it("returns the etag of its own multipart write, though another write replaces the object at once", async () => {
    const { objects, store, races } = objectsUnderTest();
    races.afterWrite = true;
    const big = new Uint8Array(20 * 1024 * 1024).fill(7);

    const info = await objects.put("big.bin", big);

    expect(info.etag).toBe('"v1"');
    expect(info.size).toBe(big.byteLength);
    expect(store.get("big.bin")?.etag).toBe('"v2"');
  });

  it("makes no Head call", async () => {
    const { objects, client } = objectsUnderTest();

    await objects.put("a.txt", "hello");
    await objects.put("big.bin", new Uint8Array(20 * 1024 * 1024));

    expect(client.head).not.toHaveBeenCalled();
  });

  it("returns the info of the version this write stored, with no upload time", async () => {
    const { objects } = objectsUnderTest();

    const info = await objects.put("a.txt", "hello", {
      contentType: "text/plain",
      metadata: { owner: "ada" },
    });

    expect(info).toEqual({
      key: "a.txt",
      size: 5,
      etag: '"v1"',
      contentType: "text/plain",
      uploadedAt: undefined,
      metadata: { owner: "ada" },
    });
  });

  it("writes application/octet-stream when the caller names no content type", async () => {
    const { objects, store, client } = objectsUnderTest();

    const small = await objects.put("a.bin", "hello");
    const big = await objects.put("big.bin", new Uint8Array(20 * 1024 * 1024));

    expect(small.contentType).toBe("application/octet-stream");
    expect(store.get("a.bin")?.contentType).toBe("application/octet-stream");
    expect(client.sign.mock.calls[0]?.[0]).toMatchObject({
      constraints: { contentType: "application/octet-stream" },
    });
    expect(big.contentType).toBe("application/octet-stream");
    expect(client.createMultipart.mock.calls[0]?.[0]).toMatchObject({
      contentType: "application/octet-stream",
    });
  });

  it("refuses a write the store answered without its etag", async () => {
    const { objects, races } = objectsUnderTest();
    races.etagless = true;

    await expect(objects.put("a.txt", "hello")).rejects.toThrow(
      'the store answered the write of "a.txt" without its etag',
    );
    await expect(objects.put("big.bin", new Uint8Array(20 * 1024 * 1024))).rejects.toThrow(
      'the store answered the write of "big.bin" without its etag',
    );
  });
});

describe("get", () => {
  it("reads the bytes back as text, json and bytes", async () => {
    const { objects } = objectsUnderTest();
    await objects.put("a.json", JSON.stringify({ ok: true }));

    const read = await objects.get("a.json");

    expect(read).toBeDefined();
    expect(await read?.text()).toBe('{"ok":true}');
    expect(await objects.get("a.json").then((o) => o?.json())).toEqual({ ok: true });
    expect(await objects.get("a.json").then((o) => o?.bytes())).toBeInstanceOf(Uint8Array);
  });

  it("is undefined for an object that is not there", async () => {
    const { objects } = objectsUnderTest();

    expect(await objects.get("gone.txt")).toBeUndefined();
  });

  it("never answers one version's info with another version's bytes", async () => {
    const { objects, races } = objectsUnderTest();
    await objects.put("a.txt", "hello");
    races.beforeRead = 1;

    const read = await objects.get("a.txt");

    expect(await read?.text()).toBe("another writer's bytes");
    expect(read?.info.etag).toBe('"v2"');
    expect(read?.info.size).toBe("another writer's bytes".length);
  });

  it("reads a range of the version its info describes", async () => {
    const { objects, races } = objectsUnderTest();
    await objects.put("a.txt", "hello");
    races.beforeRead = 1;

    const read = await objects.get("a.txt", { range: { offset: 0, length: 7 } });

    expect(await read?.text()).toBe("another");
    expect(read?.info.etag).toBe('"v2"');
  });

  it("gives up on an object that changes under every read", async () => {
    const { objects, client, races } = objectsUnderTest();
    await objects.put("a.txt", "hello");
    races.beforeRead = Number.POSITIVE_INFINITY;

    const reading = objects.get("a.txt");

    await expect(reading).rejects.toBeInstanceOf(PreconditionFailedError);
    await expect(reading).rejects.toThrow('"a.txt" changed while it was read');
    expect(client.head).toHaveBeenCalledTimes(3);
  });
});

describe("head", () => {
  it("is the object's info, or undefined", async () => {
    const { objects } = objectsUnderTest();
    await objects.put("a.txt", "hello");

    expect((await objects.head("a.txt"))?.size).toBe(5);
    expect(await objects.head("gone.txt")).toBeUndefined();
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
    await expect(objects.copy("gone.txt", "c.txt")).rejects.toMatchObject({
      cause: expect.any(RuntimeCallError),
    });
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

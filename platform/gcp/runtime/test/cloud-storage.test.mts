import { expect, test } from "vitest";
import { newCloudStorage } from "../src/next/cloud-storage.mjs";

type Call = { url: string; init: RequestInit };
type Script = (call: Call) => Response | Promise<Response>;

const metadataPath = "/computeMetadata/v1/instance/service-accounts/default/token";

function token(value: string, expiresIn = 3600): Response {
  return Response.json({ access_token: value, expires_in: expiresIn });
}

function stub(storage: Script[], tokens: string[] = ["t1", "t2"]) {
  const calls: Call[] = [];
  const storageCalls: Call[] = [];
  const metadataCalls: Call[] = [];
  let storageIndex = 0;
  let tokenIndex = 0;
  const fetchStub = (async (input: string | URL | Request, init: RequestInit = {}) => {
    const call = { url: String(input), init };
    calls.push(call);
    if (call.url.includes(metadataPath)) {
      metadataCalls.push(call);
      return token(tokens[tokenIndex++] ?? "late");
    }
    storageCalls.push(call);
    const script = storage[storageIndex++];
    if (!script) throw new Error("unscripted storage call");
    return script(call);
  }) as typeof fetch;
  return { fetch: fetchStub, calls, storageCalls, metadataCalls };
}

function reply(status: number, body = "", headers: Record<string, string> = {}): Script {
  return () => new Response(status === 304 ? null : body, { status, headers });
}

function header(call: Call, name: string): string | null {
  return new Headers(call.init.headers).get(name);
}

test("a read sends the metadata server's token and returns the body with its generation", async () => {
  const s = stub([reply(200, '{"a":1}', { "x-goog-generation": "7" })]);
  const storage = newCloudStorage({ bucket: "bkt", fetch: s.fetch });

  const result = await storage.read("a/b.json");

  expect(result).toEqual({ status: "found", body: '{"a":1}', generation: "7" });
  expect(s.calls[0]?.url).toBe(`http://metadata.google.internal${metadataPath}`);
  expect(header(s.calls[0] as Call, "Metadata-Flavor")).toBe("Google");
  expect(s.calls[1]?.url).toBe(
    "https://storage.googleapis.com/storage/v1/b/bkt/o/a%2Fb.json?alt=media",
  );
  expect(header(s.calls[1] as Call, "Authorization")).toBe("Bearer t1");
});

test("a read of an object that does not exist is absent", async () => {
  const s = stub([reply(404)]);
  const storage = newCloudStorage({ bucket: "bkt", fetch: s.fetch });

  expect(await storage.read("missing.json")).toEqual({ status: "absent" });
});

test("a read answered with no generation is refused", async () => {
  const s = stub([reply(200, "x")]);
  const storage = newCloudStorage({ bucket: "bkt", fetch: s.fetch });

  await expect(storage.read("a.json")).rejects.toThrow(
    "ocel: Cloud Storage answered a.json with no generation",
  );
});

test("a read conditioned on the generation it already holds is unchanged", async () => {
  const s = stub([reply(304)]);
  const storage = newCloudStorage({ bucket: "bkt", fetch: s.fetch });

  const result = await storage.read("a.json", { ifGenerationNotMatch: "7" });

  expect(result).toEqual({ status: "unchanged" });
  expect(s.storageCalls[0]?.url).toContain("ifGenerationNotMatch=7");
});

test("a write conditioned on a generation another writer replaced is lost", async () => {
  const s = stub([reply(412)]);
  const storage = newCloudStorage({ bucket: "bkt", fetch: s.fetch });

  const result = await storage.write("a.json", "{}", { ifGenerationMatch: "3" });

  expect(result).toEqual({ status: "lost" });
  expect(s.storageCalls).toHaveLength(1);
});

test("a create-only write is sent with ifGenerationMatch=0", async () => {
  const s = stub([() => Response.json({ generation: "1" })]);
  const storage = newCloudStorage({ bucket: "bkt", fetch: s.fetch });

  const result = await storage.write("a/b.json", '{"x":1}', { ifGenerationMatch: "0" });

  expect(result).toEqual({ status: "written", generation: "1" });
  const call = s.storageCalls[0] as Call;
  expect(call.url).toBe(
    "https://storage.googleapis.com/upload/storage/v1/b/bkt/o?uploadType=media&name=a%2Fb.json&ifGenerationMatch=0",
  );
  expect(call.init.method).toBe("POST");
  expect(call.init.body).toBe('{"x":1}');
  expect(header(call, "Content-Type")).toBe("application/json");
});

test("a throttled write is retried with backoff until it lands", async () => {
  const s = stub([reply(429), reply(503), () => Response.json({ generation: "9" })]);
  const sleeps: number[] = [];
  const storage = newCloudStorage({
    bucket: "bkt",
    fetch: s.fetch,
    random: () => 0.999,
    sleep: async (ms) => {
      sleeps.push(ms);
    },
  });

  const result = await storage.write("a.json", "{}");

  expect(result).toEqual({ status: "written", generation: "9" });
  expect(s.storageCalls).toHaveLength(3);
  expect(sleeps).toHaveLength(2);
  expect(sleeps[0]).toBeLessThanOrEqual(100);
  expect(sleeps[1]).toBeLessThanOrEqual(200);
});

test("a request that keeps failing stops after four attempts and names the object, not the token", async () => {
  const s = stub([reply(503), reply(503), reply(503), reply(503)], ["secret-token"]);
  const storage = newCloudStorage({ bucket: "bkt", fetch: s.fetch, sleep: async () => {} });

  const failure = await storage.read("a/b.json").then(
    () => new Error("resolved"),
    (error: Error) => error,
  );

  expect(s.storageCalls).toHaveLength(4);
  expect(failure.message).toContain("bkt/a/b.json");
  expect(failure.message).toContain("503");
  expect(failure.message).not.toContain("secret-token");
});

test("any other client error fails at once", async () => {
  const s = stub([reply(404), reply(403)]);
  const storage = newCloudStorage({ bucket: "bkt", fetch: s.fetch, sleep: async () => {} });

  await expect(storage.write("a.json", "{}")).rejects.toThrow(
    "ocel: Cloud Storage write of bkt/a.json failed: 404",
  );
  await expect(storage.read("a.json")).rejects.toThrow("failed: 403");
  expect(s.storageCalls).toHaveLength(2);
});

test("a request that hangs is abandoned after its timeout and tried again", async () => {
  const hang: Script = ({ init }) =>
    new Promise<Response>((_, reject) => {
      init.signal?.addEventListener("abort", () => reject(new Error("aborted")));
    });
  const s = stub([hang, reply(200, "ok", { "x-goog-generation": "2" })]);
  const storage = newCloudStorage({
    bucket: "bkt",
    fetch: s.fetch,
    requestTimeoutMs: 50,
    sleep: async () => {},
  });

  const result = await storage.read("a.json");

  expect(result).toEqual({ status: "found", body: "ok", generation: "2" });
  expect(s.storageCalls).toHaveLength(2);
});

test("a body that stalls after the headers arrived is abandoned after the timeout and tried again", async () => {
  const stalled: Script = ({ init }) =>
    new Response(
      new ReadableStream({
        start(controller) {
          init.signal?.addEventListener("abort", () =>
            controller.error(new DOMException("aborted", "AbortError")),
          );
        },
      }),
      { status: 200, headers: { "x-goog-generation": "1" } },
    );
  const s = stub([stalled, reply(200, "ok", { "x-goog-generation": "2" })]);
  const storage = newCloudStorage({
    bucket: "bkt",
    fetch: s.fetch,
    requestTimeoutMs: 50,
    sleep: async () => {},
  });

  const result = await storage.read("a.json");

  expect(result).toEqual({ status: "found", body: "ok", generation: "2" });
  expect(s.storageCalls).toHaveLength(2);
});

test("a write response that is not json fails at once instead of being retried", async () => {
  const s = stub([reply(200, "<html>")]);
  const storage = newCloudStorage({ bucket: "bkt", fetch: s.fetch, sleep: async () => {} });

  await expect(storage.write("a.json", "{}")).rejects.toThrow(SyntaxError);
  expect(s.storageCalls).toHaveLength(1);
});

test("a conditional write whose first attempt landed unseen is written when its retry meets 412 and the object holds its body", async () => {
  const s = stub([reply(503), reply(412), reply(200, '{"x":1}', { "x-goog-generation": "7" })]);
  const storage = newCloudStorage({ bucket: "bkt", fetch: s.fetch, sleep: async () => {} });

  const result = await storage.write("a.json", '{"x":1}', { ifGenerationMatch: "0" });

  expect(result).toEqual({ status: "written", generation: "7" });
});

test("a conditional write whose retry meets 412 and finds another body is lost", async () => {
  const s = stub([reply(503), reply(412), reply(200, '{"x":2}', { "x-goog-generation": "7" })]);
  const storage = newCloudStorage({ bucket: "bkt", fetch: s.fetch, sleep: async () => {} });

  const result = await storage.write("a.json", '{"x":1}', { ifGenerationMatch: "0" });

  expect(result).toEqual({ status: "lost" });
});

test("the token is fetched once and reused until a minute before it expires", async () => {
  const found = () => reply(200, "x", { "x-goog-generation": "1" })({} as Call);
  const s = stub([found, found, found]);
  let now = 0;
  const storage = newCloudStorage({ bucket: "bkt", fetch: s.fetch, now: () => now });

  await storage.read("a.json");
  now = 3_539_000;
  await storage.read("a.json");
  expect(s.metadataCalls).toHaveLength(1);

  now = 3_541_000;
  await storage.read("a.json");
  expect(s.metadataCalls).toHaveLength(2);
});

test("two concurrent first reads share one token fetch", async () => {
  const found = () => reply(200, "x", { "x-goog-generation": "1" })({} as Call);
  const s = stub([found, found]);
  const storage = newCloudStorage({ bucket: "bkt", fetch: s.fetch });

  await Promise.all([storage.read("a.json"), storage.read("b.json")]);

  expect(s.metadataCalls).toHaveLength(1);
});

test("a token Storage refuses is fetched again and the request retried once", async () => {
  const s = stub([reply(401), reply(200, "x", { "x-goog-generation": "1" })]);
  const storage = newCloudStorage({ bucket: "bkt", fetch: s.fetch });

  const result = await storage.read("a.json");

  expect(result.status).toBe("found");
  expect(s.metadataCalls).toHaveLength(2);
  expect(s.storageCalls).toHaveLength(2);
  expect(header(s.storageCalls[1] as Call, "Authorization")).toBe("Bearer t2");
});

test("a failed token fetch names the status and never the token", async () => {
  const fetchStub = (async () => new Response("nope", { status: 500 })) as typeof fetch;
  const storage = newCloudStorage({ bucket: "bkt", fetch: fetchStub });

  await expect(storage.read("a.json")).rejects.toThrow(
    "ocel: the metadata server gave no Cloud Storage token: 500",
  );
});

test("a metadata server that cannot be reached is named in a read's failure", async () => {
  const fetchStub = (async () => {
    throw new TypeError("fetch failed");
  }) as typeof fetch;
  const storage = newCloudStorage({ bucket: "bkt", fetch: fetchStub, sleep: async () => {} });

  await expect(storage.read("a.json")).rejects.toThrow(
    "ocel: Cloud Storage read of bkt/a.json failed: ocel: the metadata server could not be reached for a Cloud Storage token: fetch failed",
  );
});

test("an emulator endpoint is addressed with no token", async () => {
  const s = stub([reply(200, "x", { "x-goog-generation": "1" })]);
  const storage = newCloudStorage({
    bucket: "bkt",
    endpoint: "http://127.0.0.1:4588/",
    fetch: s.fetch,
  });

  await storage.read("a.json");

  expect(s.metadataCalls).toHaveLength(0);
  expect(header(s.storageCalls[0] as Call, "Authorization")).toBeNull();
  expect(s.storageCalls[0]?.url.startsWith("http://127.0.0.1:4588/storage/v1/b/")).toBe(true);
});

test("an object name keeps its slashes as part of the name", async () => {
  const s = stub([reply(404)]);
  const storage = newCloudStorage({ bucket: "bkt", fetch: s.fetch });

  await storage.read("cache/shop/web/x.json");

  expect(new URL(s.storageCalls[0]?.url as string).pathname).toMatch(
    /\/o\/cache%2Fshop%2Fweb%2Fx\.json$/,
  );
});

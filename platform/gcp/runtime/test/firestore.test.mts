import { expect, test } from "vitest";
import { newFirestore } from "../src/next/firestore.mjs";

type Call = { url: string; init: RequestInit };
type Script = (call: Call) => Response;

const metadataPath = "/computeMetadata/v1/instance/service-accounts/default/token";
const database = "projects/p/databases/ocel-production-tags";

function stub(scripts: Script[]) {
  const calls: Call[] = [];
  const firestoreCalls: Call[] = [];
  let index = 0;
  const fetchStub = (async (input: string | URL | Request, init: RequestInit = {}) => {
    const call = { url: String(input), init };
    calls.push(call);
    if (call.url.includes(metadataPath)) {
      return Response.json({ access_token: "t1", expires_in: 3600 });
    }
    firestoreCalls.push(call);
    const script = scripts[index++];
    if (!script) throw new Error("unscripted firestore call");
    return script(call);
  }) as typeof fetch;
  return { fetch: fetchStub, calls, firestoreCalls };
}

const reply =
  (status: number, body: unknown = {}): Script =>
  () =>
    Response.json(body, { status });

const noSleep = async () => {};

test("a commit is sent to the named database with the metadata server's token", async () => {
  const s = stub([reply(200, { commitTime: "2026-01-01T00:00:00Z", writeResults: [{}] })]);
  const firestore = newFirestore({ database, fetch: s.fetch, sleep: noSleep });

  const result = await firestore.commit([{ delete: "x" }]);

  expect(result.commitTime).toBe("2026-01-01T00:00:00Z");
  const call = s.firestoreCalls[0] as Call;
  expect(call.url).toBe(`https://firestore.googleapis.com/v1/${database}/documents:commit`);
  expect(call.init.method).toBe("POST");
  expect(new Headers(call.init.headers).get("Authorization")).toBe("Bearer t1");
  expect(JSON.parse(call.init.body as string)).toEqual({ writes: [{ delete: "x" }] });
});

test("a query's documents and read time are returned from the streamed answer", async () => {
  const s = stub([
    reply(200, [
      { readTime: "2026-01-01T00:00:05Z", document: { name: "d/1", fields: { a: 1 } } },
      { document: { name: "d/2", fields: {} } },
      { readTime: "2026-01-01T00:00:06Z" },
    ]),
  ]);
  const firestore = newFirestore({ database, fetch: s.fetch, sleep: noSleep });

  const result = await firestore.runQuery({ from: [{ collectionId: "tags" }] });

  expect(result.documents.map((d) => d.name)).toEqual(["d/1", "d/2"]);
  expect(result.readTime).toBe("2026-01-01T00:00:06Z");
  expect(s.firestoreCalls[0]?.url).toBe(
    `https://firestore.googleapis.com/v1/${database}/documents:runQuery`,
  );
  expect(JSON.parse(s.firestoreCalls[0]?.init.body as string)).toEqual({
    structuredQuery: { from: [{ collectionId: "tags" }] },
  });
});

test("an answer to a query that carries no read time is refused", async () => {
  const s = stub([reply(200, [])]);
  const firestore = newFirestore({ database, fetch: s.fetch, sleep: noSleep });

  await expect(firestore.runQuery({})).rejects.toThrow(/read time/);
});

test("a contended or throttled commit is retried with backoff until it lands", async () => {
  const s = stub([
    reply(409),
    reply(429),
    reply(200, { commitTime: "2026-01-01T00:00:00Z", writeResults: [] }),
  ]);
  const sleeps: number[] = [];
  const firestore = newFirestore({
    database,
    fetch: s.fetch,
    sleep: async (ms) => {
      sleeps.push(ms);
    },
    random: () => 0.5,
  });

  await firestore.commit([]);

  expect(s.firestoreCalls).toHaveLength(3);
  expect(sleeps).toEqual([50, 100]);
});

test("a call that keeps failing stops after four attempts and names the database, not the token", async () => {
  const s = stub([reply(503), reply(503), reply(503), reply(503)]);
  const firestore = newFirestore({ database, fetch: s.fetch, sleep: noSleep });

  const failure = await firestore.commit([]).catch((error: Error) => error);

  expect(s.firestoreCalls).toHaveLength(4);
  expect((failure as Error).message).toContain(database);
  expect((failure as Error).message).toContain("503");
  expect((failure as Error).message).not.toContain("t1");
});

test("a request the database refuses outright is not retried", async () => {
  const s = stub([reply(403)]);
  const firestore = newFirestore({ database, fetch: s.fetch, sleep: noSleep });

  await expect(firestore.commit([])).rejects.toThrow(/403/);
  expect(s.firestoreCalls).toHaveLength(1);
});

test("a rejected token is dropped and the call retried once with a fresh one", async () => {
  const s = stub([reply(401), reply(200, { commitTime: "c", writeResults: [] })]);
  const firestore = newFirestore({ database, fetch: s.fetch, sleep: noSleep });

  await firestore.commit([]);

  expect(s.calls.filter((c) => c.url.includes(metadataPath))).toHaveLength(2);
  expect(s.firestoreCalls).toHaveLength(2);
});

test("an emulator endpoint is addressed with no token", async () => {
  const s = stub([reply(200, { commitTime: "c", writeResults: [] })]);
  const firestore = newFirestore({
    database,
    endpoint: "http://127.0.0.1:8080/",
    fetch: s.fetch,
    sleep: noSleep,
  });

  await firestore.commit([]);

  expect(s.calls).toHaveLength(1);
  expect(s.calls[0]?.url).toBe(`http://127.0.0.1:8080/v1/${database}/documents:commit`);
  expect(new Headers(s.calls[0]?.init.headers).get("Authorization")).toBeNull();
});

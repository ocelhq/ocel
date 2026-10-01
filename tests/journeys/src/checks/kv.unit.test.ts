import { describe, expect, it } from "bun:test";
import type { Phase } from "../matrix/types";
import type { CheckContext, Fetch } from "./context";
import { kvPersistenceCheck } from "./kv";

const BASE = "https://web-j-1-kv-node.journey.test";

type Request = { method: string; path: string };

function store(): { fetch: Fetch; requests: Request[] } {
  const values = new Map<string, string>();
  const requests: Request[] = [];
  const fetch: Fetch = async (input, init) => {
    const path = new URL(String(input)).pathname;
    const method = init?.method ?? "GET";
    requests.push({ method, path });
    if (path === "/api/kv/ping") {
      return Response.json({ pong: "PONG" });
    }
    if (method === "PUT") {
      values.set(path, (JSON.parse(String(init?.body)) as { value: string }).value);
      return new Response(null, { status: 204 });
    }
    const value = values.get(path);
    return value === undefined
      ? Response.json({ error: "missing" }, { status: 404 })
      : Response.json({ value });
  };
  return { fetch, requests };
}

function context(fetch: Fetch, phase: Phase, notes: Map<string, string>): CheckContext {
  return {
    app: "web",
    baseUrl: BASE,
    greeting: "journey-hello",
    maxRequestBodyBytes: 1024,
    phase,
    notes,
    fetch,
    readExposed: async () => "",
  };
}

function failure(work: Promise<unknown>): Promise<string> {
  return work.then(
    () => "",
    (error: unknown) => (error as Error).message,
  );
}

describe("kvPersistenceCheck", () => {
  it("reads back after a restart the key it wrote at verify", async () => {
    const { fetch } = store();
    const notes = new Map<string, string>();
    await kvPersistenceCheck.run(context(fetch, "verify", notes));
    expect(await failure(kvPersistenceCheck.run(context(fetch, "restart", notes)))).toBe("");
  });

  it("fails a restart before which nothing was written, writing nothing itself", async () => {
    const { fetch, requests } = store();
    const said = await failure(kvPersistenceCheck.run(context(fetch, "restart", new Map())));
    expect(said).toBe("nothing was written before the restart to read back");
    expect(requests.filter((one) => one.method === "PUT")).toEqual([]);
  });

  it("fails a redeploy that lost the key written before it", async () => {
    const before = store();
    const notes = new Map<string, string>();
    await kvPersistenceCheck.run(context(before.fetch, "verify", notes));
    const said = await failure(kvPersistenceCheck.run(context(store().fetch, "redeploy", notes)));
    expect(said).toContain("the read after the redeploy answered 404");
  });
});

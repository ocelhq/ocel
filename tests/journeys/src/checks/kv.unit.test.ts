import { describe, expect, it } from "bun:test";
import type { Phase } from "../matrix/types";
import { type CheckContext, type Fetch, PASSWORD_REPORT_NONCE_HEADER } from "./context";
import {
  kvJsonInvalidStoredCheck,
  kvPasswordOutOfEnvironmentCheck,
  kvPersistenceCheck,
} from "./kv";

const BASE = "https://web-j-1-kv-node.journey.test";
const NONCE = "journey-password-report-nonce";

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
    passwordReportNonce: NONCE,
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

type Reading = "valid" | "unparseable" | "schema-invalid";

function readingOf(raw: string): Reading {
  try {
    return typeof (JSON.parse(raw) as { name?: unknown }).name === "string"
      ? "valid"
      : "schema-invalid";
  } catch {
    return "unparseable";
  }
}

function jsonEntries(refuses: Record<"json" | "lenient", Reading[]>): Fetch {
  const raws = new Map<string, string>();
  return async (input, init) => {
    const path = new URL(String(input)).pathname;
    const written = /^\/api\/kv\/raw\/(json|lenient)\/(.+)$/.exec(path);
    if (written && init?.method === "PUT") {
      raws.set(
        `${written[1]}/${written[2]}`,
        (JSON.parse(String(init.body)) as { raw: string }).raw,
      );
      return new Response(null, { status: 204 });
    }
    const read = /^\/api\/kv\/(json|lenient)\/(.+)$/.exec(path);
    const entry = read?.[1] as "json" | "lenient";
    const raw = raws.get(`${entry}/${read?.[2]}`);
    if (raw === undefined) {
      return Response.json({ error: "no such key" }, { status: 404 });
    }
    if (!refuses[entry].includes(readingOf(raw))) {
      return Response.json({ value: raw });
    }
    return entry === "json"
      ? Response.json({ error: "InvalidKVValueError" }, { status: 422 })
      : Response.json({ error: "no such key" }, { status: 404 });
  };
}

describe("kvJsonInvalidStoredCheck", () => {
  const BOTH: Reading[] = ["unparseable", "schema-invalid"];

  it("passes entries that refuse an unparseable and a schema-invalid value alike", async () => {
    const fetch = jsonEntries({ json: BOTH, lenient: BOTH });
    expect(await failure(kvJsonInvalidStoredCheck.run(context(fetch, "verify", new Map())))).toBe(
      "",
    );
  });

  for (const [entry, missed] of [
    ["json", "schema-invalid"],
    ["json", "unparseable"],
    ["lenient", "schema-invalid"],
    ["lenient", "unparseable"],
  ] as const) {
    it(`fails a ${entry} entry that reads a ${missed} stored value as valid`, async () => {
      const refuses = { json: BOTH, lenient: BOTH, [entry]: BOTH.filter((one) => one !== missed) };
      const said = await failure(
        kvJsonInvalidStoredCheck.run(context(jsonEntries(refuses), "verify", new Map())),
      );
      expect(said).toContain(`${missed} value`);
      expect(said).toContain("200");
    });
  }
});

describe("kvPasswordOutOfEnvironmentCheck", () => {
  it("asks for the password report with the nonce the cell handed the app", async () => {
    const fetch: Fetch = async (input, init) => {
      const asked = new Headers(init?.headers).get(PASSWORD_REPORT_NONCE_HEADER);
      if (new URL(String(input)).pathname !== "/api/kv/password-report" || asked !== NONCE) {
        return Response.json({ error: "forbidden" }, { status: 403 });
      }
      return Response.json({ password: "a-password-worth-hiding", environment: [] });
    };
    expect(
      await failure(kvPasswordOutOfEnvironmentCheck.run(context(fetch, "verify", new Map()))),
    ).toBe("");
  });
});

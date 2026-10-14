import { createExecutionContext } from "cloudflare:test";
import {
  derivePurgeSecret,
  type PurgeMode,
  postPurge,
  purgePath,
  purgeSignatureHeader,
} from "@platform/edge-contract/purge";
import { beforeEach, describe, expect, it } from "vitest";
import worker, { type Env } from "../src/index";
import type { RaisedTag } from "../src/purge-backlog";
import type { PointerRecordResult, ReleasesBinding } from "../src/releases";
import { makeRecord } from "./origin-deps";

const SEED = "seed-1";
const ISR_PREFIX = "production/p1/web/r0a1b2c3d/isr";
const SECRET = derivePurgeSecret(SEED, ISR_PREFIX);
const WORKERS_DEV = "https://web.acct.workers.dev";

async function sign(body: string): Promise<string> {
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(SECRET),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const mac = await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(body));
  return [...new Uint8Array(mac)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

async function purgeRequest(base: string, body: unknown = raise): Promise<Request> {
  return purgeRequestTo(`${base}${purgePath}`, body);
}

async function purgeRequestTo(url: string, body: unknown = raise): Promise<Request> {
  const text = JSON.stringify(body);
  return new Request(url, {
    method: "POST",
    headers: { [purgeSignatureHeader]: await sign(text) },
    body: text,
  });
}

const raise = {
  isrPrefix: ISR_PREFIX,
  mode: "purge",
  tags: [{ tag: "_N_T_/blog/a", raisedAt: 5_000 }],
};

let raised: { name: string; mode: PurgeMode; tags: RaisedTag[] }[];
let appReads: number;
let servedReads: { slug: string; isrPrefix: string }[];
let heldGate: string | undefined;

function binding(): ReleasesBinding {
  const answer = async (): Promise<PointerRecordResult> => {
    appReads++;
    return { kind: "no-pointer" };
  };
  return {
    readPointerRecord: answer,
    readLabelRecord: answer,
    async readServedRecord(args) {
      servedReads.push(args);
      return args.isrPrefix === ISR_PREFIX
        ? {
            kind: "record",
            record: makeRecord({
              app: "web",
              release: "build-1~fp1",
              isrPrefix: ISR_PREFIX,
              purgeSecret: SECRET,
            }),
          }
        : { kind: "not-served" };
    },
    readGate: async () => heldGate,
  };
}

function contextWithQueue() {
  const ctx = createExecutionContext();
  const queues = {
    getByName: (name: string) => ({
      raise: async (mode: PurgeMode, tags: RaisedTag[]) => void raised.push({ name, mode, tags }),
      record: async () => {},
    }),
  };
  Object.defineProperty(ctx, "exports", { value: { PurgeQueue: queues, Serve: () => ({}) } });
  return ctx;
}

function envWith(over: Partial<Env> = {}): Env {
  return { RELEASES: binding(), OCEL_SLUG: "p1", ...over };
}

beforeEach(() => {
  raised = [];
  appReads = 0;
  servedReads = [];
  heldGate = undefined;
});

describe("the gateway answering a purge on its workers.dev host", () => {
  it("accepts a signed raise for a served release and hands it to the one queue", async () => {
    const response = await worker.fetch(
      await purgeRequest(WORKERS_DEV),
      envWith(),
      contextWithQueue(),
    );

    expect(response.status).toBe(202);
    expect(servedReads).toEqual([{ slug: "p1", isrPrefix: ISR_PREFIX }]);
    expect(raised).toEqual([
      {
        name: "purge",
        mode: "purge",
        tags: [{ tag: "r0a1b2c3d|_N_T_/blog/a", raisedAt: 5_000 }],
      },
    ]);
  });

  it("queues what an origin's sender posts for the release it serves, under the release token the origin stamps its tags with", async () => {
    const ctx = contextWithQueue();
    const sender = {
      fetch: ((url: string, init?: RequestInit) =>
        worker.fetch(new Request(url, init), envWith(), ctx)) as typeof fetch,
    };

    await postPurge(sender, `${WORKERS_DEV}${purgePath}`, SECRET, ISR_PREFIX, [
      { tag: "_N_T_/blog/a", mode: "invalidate", raisedAt: 5_000 },
    ]);

    expect(raised).toEqual([
      {
        name: "purge",
        mode: "invalidate",
        tags: [{ tag: "r0a1b2c3d|_N_T_/blog/a", raisedAt: 5_000 }],
      },
    ]);
  });

  it("answers 204 for a release that is not served and raises nothing", async () => {
    const response = await worker.fetch(
      await purgeRequest(WORKERS_DEV, { ...raise, isrPrefix: "production/p1/web/r0000000a/isr" }),
      envWith(),
      contextWithQueue(),
    );

    expect(response.status).toBe(204);
    expect(raised).toEqual([]);
  });

  it("answers 401 for a request signed with the wrong secret", async () => {
    const request = await purgeRequest(WORKERS_DEV);
    const forged = new Request(request.url, {
      method: "POST",
      headers: { [purgeSignatureHeader]: "00".repeat(32) },
      body: JSON.stringify(raise),
    });

    const response = await worker.fetch(forged, envWith(), contextWithQueue());

    expect(response.status).toBe(401);
    expect(raised).toEqual([]);
  });

  it("answers 404 when the worker serves no single project to look the release up in", async () => {
    const response = await worker.fetch(
      await purgeRequest(WORKERS_DEV),
      envWith({ OCEL_SLUG: "" }),
      contextWithQueue(),
    );

    expect(response.status).toBe(404);
  });
});

describe("the shared preview entry answering a purge on its workers.dev host", () => {
  const SHARED = "https://ocel-preview-entry.acct.workers.dev";
  const sharedEnv = () =>
    envWith({ OCEL_SLUG: undefined, OCEL_PREVIEW: "1", OCEL_PREVIEW_GLOBAL: "1" });

  it("looks the release up under the project its purge URL names, and queues the raise", async () => {
    const response = await worker.fetch(
      await purgeRequestTo(`${SHARED}${purgePath}?project=p1`),
      sharedEnv(),
      contextWithQueue(),
    );

    expect(response.status).toBe(202);
    expect(servedReads).toEqual([{ slug: "p1", isrPrefix: ISR_PREFIX }]);
    expect(raised).toEqual([
      {
        name: "purge",
        mode: "purge",
        tags: [{ tag: "r0a1b2c3d|_N_T_/blog/a", raisedAt: 5_000 }],
      },
    ]);
  });

  it("answers 404 to a purge whose URL names no project", async () => {
    const response = await worker.fetch(
      await purgeRequest(SHARED),
      sharedEnv(),
      contextWithQueue(),
    );

    expect(response.status).toBe(404);
    expect(servedReads).toEqual([]);
  });

  it("is never steered to another project on a worker that serves one", async () => {
    const response = await worker.fetch(
      await purgeRequestTo(`${WORKERS_DEV}${purgePath}?project=other`),
      envWith(),
      contextWithQueue(),
    );

    expect(response.status).toBe(202);
    expect(servedReads).toEqual([{ slug: "p1", isrPrefix: ISR_PREFIX }]);
  });
});

describe("the gateway keeping app traffic off its workers.dev host", () => {
  it("answers 404 to an app request, resolving no release", async () => {
    const response = await worker.fetch(
      new Request(`${WORKERS_DEV}/blog/a`),
      envWith(),
      contextWithQueue(),
    );

    expect(response.status).toBe(404);
    expect(appReads).toBe(0);
  });

  it("serves the app there when the worker has no other host", async () => {
    await worker.fetch(
      new Request(`${WORKERS_DEV}/blog/a`),
      envWith({ OCEL_SERVES_WORKERS_DEV: "1", OCEL_APP: "web" }),
      contextWithQueue(),
    );

    expect(appReads).toBeGreaterThan(0);
  });

  it("answers a purge there on a worker whose project is protected", async () => {
    heldGate = JSON.stringify({
      key: "gate-key",
      passwordMac: "password-mac",
      bypassSecrets: [],
      allowOptions: [],
    });
    const response = await worker.fetch(
      await purgeRequest(WORKERS_DEV),
      envWith({ OCEL_SERVES_WORKERS_DEV: "1" }),
      contextWithQueue(),
    );

    expect(response.status).toBe(202);
    expect(raised).toHaveLength(1);
  });

  it("still answers a purge there when the worker serves the app there too", async () => {
    const response = await worker.fetch(
      await purgeRequest(WORKERS_DEV),
      envWith({ OCEL_SERVES_WORKERS_DEV: "1" }),
      contextWithQueue(),
    );

    expect(response.status).toBe(202);
  });
});

describe("the gateway on an app hostname", () => {
  it("leaves a POST to the purge path to the app", async () => {
    const response = await worker.fetch(
      await purgeRequest("https://shop.example.com"),
      envWith({ OCEL_APP: "web" }),
      contextWithQueue(),
    );

    expect(response.status).toBe(404);
    expect(servedReads).toEqual([]);
    expect(raised).toEqual([]);
    expect(appReads).toBeGreaterThan(0);
  });
});

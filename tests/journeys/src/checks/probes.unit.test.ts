import { describe, expect, it } from "bun:test";
import { gzipSync } from "node:zlib";
import { type CheckContext, type Fetch, SECRET_TOKEN, secretGuarded } from "./context";
import { clientAddressCheck, httpProbeChecks, publicOriginCheck, streamCheck } from "./probes";

const BASE = "https://web-j-1-deploy-node.journey.test";

function checkTitled(prefix: string) {
  const found = httpProbeChecks.find((one) => one.title.startsWith(prefix));
  if (!found) {
    throw new Error(`no probe check starting ${prefix}`);
  }
  return found;
}

function context(fetch: Fetch, baseUrl = BASE): CheckContext {
  return {
    app: "web",
    baseUrl,
    greeting: "journey-hello",
    maxRequestBodyBytes: 1024,
    phase: "verify",
    notes: new Map(),
    fetch,
    readExposed: async () => "",
    journeyNonce: "journey-nonce",
  };
}

function jsonResponse(body: unknown, init?: ResponseInit): Response {
  return new Response(JSON.stringify(body), {
    ...init,
    headers: { "content-type": "application/json", ...init?.headers },
  });
}

function trickle(chunks: string[], everyMs: number): Response {
  const encoder = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    async start(controller) {
      for (const chunk of chunks) {
        controller.enqueue(encoder.encode(chunk));
        await new Promise((resolve) => setTimeout(resolve, everyMs));
      }
      controller.close();
    },
  });
  return new Response(body, { status: 200, headers: { "content-type": "text/plain" } });
}

function failure(work: Promise<unknown>): Promise<string> {
  return work.then(
    () => "",
    (error: unknown) => (error as Error).message,
  );
}

const STREAM_CHUNKS = [
  "ocel-stream-1\n",
  "ocel-stream-2\n",
  "ocel-stream-3\n",
  "ocel-stream-4\n",
  "ocel-stream-end\n",
];

describe("the secret guard", () => {
  it("hands chunks over as they arrive instead of buffering the body first", async () => {
    const guard = secretGuarded(async () => trickle(STREAM_CHUNKS, 30));
    const res = await guard.fetch(`${BASE}/api/probes/stream`);
    const reader = res.body!.getReader();
    const arrivals: number[] = [];
    for (;;) {
      const { done } = await reader.read();
      if (done) {
        break;
      }
      arrivals.push(Date.now());
    }
    expect(new Set(arrivals).size).toBeGreaterThanOrEqual(3);
    await guard.settle();
  });

  it("fails at settle when the secret straddles two chunks of a body the row read", async () => {
    const half = Math.floor(SECRET_TOKEN.length / 2);
    const guard = secretGuarded(async () =>
      trickle([`{"a":"${SECRET_TOKEN.slice(0, half)}`, `${SECRET_TOKEN.slice(half)}"}`], 5),
    );
    const res = await guard.fetch(`${BASE}/api/probes/echo`);
    await res.text();
    expect(await failure(guard.settle())).toContain("/api/probes/echo leaked the secret");
  });

  it("fails at settle even when the row never read the body", async () => {
    const guard = secretGuarded(async () => jsonResponse({ token: SECRET_TOKEN }));
    await guard.fetch(`${BASE}/api/probes/env`);
    expect(await failure(guard.settle())).toContain("/api/probes/env leaked the secret");
  });

  it("passes a bodyless 204 through untouched", async () => {
    const guard = secretGuarded(async () => new Response(null, { status: 204 }));
    const res = await guard.fetch(`${BASE}/api/probes/status/204`);
    expect(res.status).toBe(204);
    await guard.settle();
  });
});

describe("the stream check", () => {
  it("fails when every chunk lands at once", async () => {
    const message = await failure(
      streamCheck.run(context(async () => new Response(STREAM_CHUNKS.join("")))),
    );
    expect(message).toContain("something buffered it");
  });

  it("passes when the chunks trickle in", async () => {
    await streamCheck.run(context(async () => trickle(STREAM_CHUNKS, 30)));
  });
});

describe("the cookies check", () => {
  it("fails when three Set-Cookie lines are folded into one", async () => {
    const folded = async (input: string | URL | Request) => {
      const count = Number(new URL(String(input)).searchParams.get("count"));
      const lines = Array.from({ length: count }, (_, i) => `ocel-cookie-${i + 1}=value-${i + 1}`);
      return jsonResponse({ count }, { headers: { "set-cookie": lines.join(", ") } });
    };
    const message = await failure(checkTitled("GET /api/probes/cookies").run(context(folded)));
    expect(message).toContain("3 cookie(s) arrived as");
  });
});

describe("the compression check", () => {
  it("fails when the edge compresses an already compressed body", async () => {
    const doubled: Fetch = async (_input, init) => {
      const accept = new Headers(init?.headers).get("accept-encoding") ?? "";
      const body = Buffer.from(JSON.stringify({ marker: "ocel-compress" }));
      const encoding = accept.includes("gzip") ? "gzip, gzip" : null;
      return new Response(encoding ? gzipSync(gzipSync(body)) : body, {
        headers: { ...(encoding ? { "content-encoding": encoding } : {}) },
      });
    };
    const message = await failure(checkTitled("GET /api/probes/compress").run(context(doubled)));
    expect(message).toContain("arrived encoded as gzip, gzip");
  });
});

describe("the method check", () => {
  it("fails when the edge answers 403 in place of the app's 405", async () => {
    const gateway: Fetch = async (_input, init) =>
      init?.method === "PATCH"
        ? jsonResponse({ message: "Missing Authentication Token" }, { status: 403 })
        : jsonResponse({ method: "GET" });
    const message = await failure(checkTitled("PATCH /api/probes/method").run(context(gateway)));
    expect(message).toContain("status 403");
  });
});

describe("the client address check", () => {
  const PUBLIC_TARGET = "https://198.51.100.20";
  const PRIVATE_TARGET = "http://10.248.113.128";

  function dumping(chain: (sent: string | null) => string | null, remote = "172.18.0.2"): Fetch {
    return async (_input, init) => {
      const sent = new Headers(init?.headers).get("x-forwarded-for");
      const forwarded = chain(sent);
      return jsonResponse({
        headers: forwarded === null ? {} : { "x-forwarded-for": forwarded },
        remote,
        ip: remote,
        protocol: "http",
        hostname: "web.internal",
      });
    };
  }

  const appending = (address: string) => (sent: string | null) =>
    sent ? `${sent}, ${address}` : address;

  it("fails when the forged address is all the app ever sees", async () => {
    const message = await failure(
      clientAddressCheck.run(
        context(
          dumping((sent) => sent),
          PUBLIC_TARGET,
        ),
      ),
    );
    expect(message).toContain("no hop appended the client address");
  });

  it("fails when a public target hands the app a private client address", async () => {
    const message = await failure(
      clientAddressCheck.run(context(dumping(appending("172.31.4.9")), PUBLIC_TARGET)),
    );
    expect(message).toContain('private "172.31.4.9"');
  });

  it("fails when the client address is only the hop in front of the app", async () => {
    const message = await failure(
      clientAddressCheck.run(
        context(dumping(appending("10.248.113.1"), "10.248.113.1"), PRIVATE_TARGET),
      ),
    );
    expect(message).toContain("the client address is the hop in front of the app");
  });

  it("passes on a private target when the runner's own private address comes through", async () => {
    await clientAddressCheck.run(context(dumping(appending("10.248.113.1")), PRIVATE_TARGET));
  });

  it("passes when every hop appends the runner's public address", async () => {
    await clientAddressCheck.run(context(dumping(appending("198.51.100.7")), PUBLIC_TARGET));
  });

  it("passes on a .localhost target when the runner's own private address comes through", async () => {
    await clientAddressCheck.run(
      context(dumping(appending("10.248.113.1")), "http://web.localhost"),
    );
  });

  it("passes on a target whose name does not resolve when a private address comes through", async () => {
    await clientAddressCheck.run(
      context(dumping(appending("10.248.113.1")), "https://nothing.invalid"),
    );
  });
});

describe("the public origin check", () => {
  it("fails when the app is told the internal host over plain http", async () => {
    const message = await failure(
      publicOriginCheck.run(
        context(async () =>
          jsonResponse({
            headers: { host: "127.0.0.1:3000", "x-forwarded-proto": "http" },
            remote: null,
            ip: null,
            protocol: "http",
            hostname: "127.0.0.1",
          }),
        ),
      ),
    );
    expect(message).toContain("https");
  });
});

describe("the oversized cookie check", () => {
  function refusing(cookieStatus: number, headers: Record<string, string>): Fetch {
    return async (input, init) => {
      if (new Headers(init?.headers).has("cookie")) {
        return new Response("Request Header Or Cookie Too Large", {
          status: cookieStatus,
          headers,
        });
      }
      const pad = new URL(String(input)).searchParams.get("pad");
      return jsonResponse({ query: { pad } });
    };
  }

  it("passes when a front ahead of ocel refuses the cookie with its documented 400", async () => {
    await checkTitled("GET /api/probes/echo takes oversized cookies").run(
      context(refusing(400, {})),
    );
  });

  it("fails when ocel itself answers the cookie with a 400", async () => {
    const message = await failure(
      checkTitled("GET /api/probes/echo takes oversized cookies").run(
        context(refusing(400, { "x-ocel-edge": "box" })),
      ),
    );
    expect(message).toContain("twelve kilobytes of cookie answered 400 from box");
  });
});

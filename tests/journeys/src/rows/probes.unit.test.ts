import { describe, expect, it } from "bun:test";
import { gzipSync } from "node:zlib";
import { type ContractContext, type Fetch, SECRET_TOKEN, secretGuarded } from "../contract";
import { CLIENT_IP_ROW, PUBLIC_ORIGIN_ROW, probeRows, STREAM_ROW } from "./probes";

const BASE = "https://web-j-1-deploy-node.journey.test";

function row(title: string) {
  const found = probeRows.find((one) => one.title === title);
  if (!found) {
    throw new Error(`no probe row titled ${title}`);
  }
  return found;
}

function rowStarting(prefix: string) {
  const found = probeRows.find((one) => one.title.startsWith(prefix));
  if (!found) {
    throw new Error(`no probe row starting ${prefix}`);
  }
  return found;
}

function context(fetch: Fetch): ContractContext {
  return {
    app: "web",
    baseUrl: BASE,
    greeting: "journey-hello",
    largeBodyBytes: 1024,
    leg: "contract",
    notes: new Map(),
    fetch,
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

describe("the stream row", () => {
  it("fails when every chunk lands at once", async () => {
    const message = await failure(
      row(STREAM_ROW).run(context(async () => new Response(STREAM_CHUNKS.join("")))),
    );
    expect(message).toContain("something buffered it");
  });

  it("passes when the chunks trickle in", async () => {
    await row(STREAM_ROW).run(context(async () => trickle(STREAM_CHUNKS, 30)));
  });
});

describe("the cookies row", () => {
  it("fails when three Set-Cookie lines are folded into one", async () => {
    const folded = async (input: string | URL | Request) => {
      const count = Number(new URL(String(input)).searchParams.get("count"));
      const lines = Array.from({ length: count }, (_, i) => `ocel-cookie-${i + 1}=value-${i + 1}`);
      return jsonResponse({ count }, { headers: { "set-cookie": lines.join(", ") } });
    };
    const message = await failure(rowStarting("GET /api/probes/cookies").run(context(folded)));
    expect(message).toContain("3 cookie(s) arrived as");
  });
});

describe("the compression row", () => {
  it("fails when the edge compresses an already compressed body", async () => {
    const doubled: Fetch = async (_input, init) => {
      const accept = new Headers(init?.headers).get("accept-encoding") ?? "";
      const body = Buffer.from(JSON.stringify({ marker: "ocel-compress" }));
      const encoding = accept.includes("gzip") ? "gzip, gzip" : null;
      return new Response(encoding ? gzipSync(gzipSync(body)) : body, {
        headers: { ...(encoding ? { "content-encoding": encoding } : {}) },
      });
    };
    const message = await failure(rowStarting("GET /api/probes/compress").run(context(doubled)));
    expect(message).toContain("arrived encoded as gzip, gzip");
  });
});

describe("the method row", () => {
  it("fails when the edge answers 403 in place of the app's 405", async () => {
    const gateway: Fetch = async (_input, init) =>
      init?.method === "PATCH"
        ? jsonResponse({ message: "Missing Authentication Token" }, { status: 403 })
        : jsonResponse({ method: "GET" });
    const message = await failure(rowStarting("PATCH /api/probes/method").run(context(gateway)));
    expect(message).toContain("status 403");
  });
});

describe("the client address row", () => {
  function dumping(chain: (sent: string | null) => string | null): Fetch {
    return async (_input, init) => {
      const sent = new Headers(init?.headers).get("x-forwarded-for");
      const forwarded = chain(sent);
      return jsonResponse({
        headers: forwarded === null ? {} : { "x-forwarded-for": forwarded },
        remote: "10.0.0.2",
        ip: "10.0.0.2",
        protocol: "http",
        hostname: "web.internal",
      });
    };
  }

  it("fails when the forged address is all the app ever sees", async () => {
    const message = await failure(row(CLIENT_IP_ROW).run(context(dumping((sent) => sent))));
    expect(message).toContain("no hop appended the client address");
  });

  it("fails when the appended address is the proxy's private one", async () => {
    const message = await failure(
      row(CLIENT_IP_ROW).run(
        context(dumping((sent) => (sent ? `${sent}, 172.31.4.9` : "172.31.4.9"))),
      ),
    );
    expect(message).toContain('private "172.31.4.9"');
  });

  it("passes when every hop appends the runner's public address", async () => {
    await row(CLIENT_IP_ROW).run(
      context(dumping((sent) => (sent ? `${sent}, 198.51.100.7` : "198.51.100.7"))),
    );
  });
});

describe("the public origin row", () => {
  it("fails when the app is told the internal host over plain http", async () => {
    const message = await failure(
      row(PUBLIC_ORIGIN_ROW).run(
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

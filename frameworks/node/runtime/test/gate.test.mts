import http from "node:http";
import type { AddressInfo } from "node:net";
import { type Gate, hashPassword, LOGIN_PATH } from "@platform/edge-contract/gate";
import { afterEach, describe, expect, it } from "vitest";
import rawVectors from "../../../../pkg/edge/edgeconformance/gate.json" with { type: "json" };
import {
  admitGate,
  admitGateUpgrade,
  GATE_CACHE_MS,
  type GateSource,
  newGateSource,
  openGate,
} from "../src/gate.mjs";

interface Case {
  name: string;
  gate?: Partial<Gate>;
  request: {
    method: string;
    host: string;
    target: string;
    revalidateTokenDigest?: string;
    headers: Record<string, string | string[]>;
  };
  expect: {
    forward?: boolean;
    forwardHeaders?: Record<string, string>;
    forwardHeadersAbsent?: string[];
    responseHeaders?: Record<string, string>;
    status?: number;
    headers?: Record<string, string>;
    headersAbsent?: string[];
    bodyContains?: string[];
    bodyExcludes?: string[];
  };
}

const vectors = rawVectors as unknown as { nowSeconds: number; gate: Gate; cases: Case[] };

const closing: http.Server[] = [];
afterEach(() => {
  for (const server of closing.splice(0)) server.close();
});

interface Reach {
  status: number;
  headers: http.IncomingHttpHeaders;
  body: string;
  reachedApp: boolean;
  heard: http.IncomingHttpHeaders;
}

async function serveAndReach(
  source: GateSource,
  method: string,
  target: string,
  headers: http.OutgoingHttpHeaders,
  app: (res: http.ServerResponse) => void = (res) => res.end("served by the app"),
  revalidateTokenDigest?: string,
): Promise<Reach> {
  let reachedApp = false;
  let heard: http.IncomingHttpHeaders = {};
  const server = http.createServer(async (req, res) => {
    if (!(await admitGate(source, req, res, req.headers.host, revalidateTokenDigest))) return;
    reachedApp = true;
    heard = { ...req.headers };
    app(res);
  });
  closing.push(server);
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as AddressInfo;
  return new Promise((resolve, reject) => {
    const req = http.request({ host: "127.0.0.1", port, method, path: target, headers }, (res) => {
      const chunks: Buffer[] = [];
      res.on("data", (chunk: Buffer) => chunks.push(chunk));
      res.on("end", () =>
        resolve({
          status: res.statusCode ?? 0,
          headers: res.headers,
          body: Buffer.concat(chunks).toString("utf8"),
          reachedApp,
          heard,
        }),
      );
    });
    req.on("error", reject);
    req.end();
  });
}

function gateSource(contents: () => string | Error, nowMs: () => number): GateSource {
  return newGateSource(async () => {
    const read = contents();
    if (read instanceof Error) throw read;
    return read;
  }, nowMs);
}

function sourceOf(gate: Gate | null, nowMs = vectors.nowSeconds * 1000): GateSource {
  return gateSource(
    () => JSON.stringify(gate),
    () => nowMs,
  );
}

function cannotCrossNodesHttpClient(example: Case): boolean {
  const wire = [example.request.target, ...Object.values(example.request.headers).flat()];
  return (
    wire.some((text) => [...text].some((char) => char.charCodeAt(0) > 0x7f)) ||
    example.request.method !== example.request.method.toUpperCase()
  );
}

describe("a service that mounts a gate answers every conformance vector", () => {
  for (const example of vectors.cases) {
    it(example.name, async () => {
      if (cannotCrossNodesHttpClient(example)) return;
      const gate: Gate = { ...vectors.gate, ...example.gate };
      const wire: http.OutgoingHttpHeaders = { host: example.request.host };
      for (const [name, values] of Object.entries(example.request.headers)) wire[name] = values;
      const reach = await serveAndReach(
        sourceOf(gate),
        example.request.method,
        example.request.target,
        wire,
        undefined,
        example.request.revalidateTokenDigest,
      );

      if (example.expect.forward) {
        expect(reach.reachedApp, "reached the app").toBe(true);
        for (const [name, want] of Object.entries(example.expect.forwardHeaders ?? {}))
          expect(reach.heard[name.toLowerCase()], name).toBe(want);
        for (const name of example.expect.forwardHeadersAbsent ?? [])
          expect(reach.heard[name.toLowerCase()], name).toBeUndefined();
        for (const [name, want] of Object.entries(example.expect.responseHeaders ?? {}))
          expect([reach.headers[name.toLowerCase()]].flat().join(", "), name).toBe(want);
        return;
      }
      expect(reach.reachedApp, "reached the app").toBe(false);
      expect(reach.status).toBe(example.expect.status);
      for (const [name, want] of Object.entries(example.expect.headers ?? {}))
        expect([reach.headers[name.toLowerCase()]].flat().join(", "), name).toBe(want);
      for (const name of example.expect.headersAbsent ?? [])
        expect(reach.headers[name.toLowerCase()], name).toBeUndefined();
      if (example.request.method !== "HEAD") {
        for (const want of example.expect.bodyContains ?? []) expect(reach.body).toContain(want);
        for (const unwanted of example.expect.bodyExcludes ?? [])
          expect(reach.body).not.toContain(unwanted);
      }
    });
  }
});

describe("a mounted gate that changes", () => {
  it("is read again once the cache lapses, so a rotated key refuses the old session", async () => {
    const gateWith = async (key: string): Promise<Gate> => ({
      key,
      passwordMac: await hashPassword(key, "pw"),
      bypassSecrets: [],
      allowOptions: [],
    });
    let mounted = await gateWith("first");
    let nowMs = vectors.nowSeconds * 1000;
    const source = gateSource(
      () => JSON.stringify(mounted),
      () => nowMs,
    );
    const basic = `Basic ${Buffer.from(":pw").toString("base64")}`;
    const login = await serveAndReach(source, "POST", LOGIN_PATH, {
      host: "app.preview.example.com",
      authorization: basic,
    });
    const cookie = String(login.headers["set-cookie"]).split(";")[0]!;
    const session = { host: "app.preview.example.com", cookie };

    expect((await serveAndReach(source, "GET", "/page", session)).status).toBe(200);
    mounted = await gateWith("rotated");
    nowMs += GATE_CACHE_MS;
    expect((await serveAndReach(source, "GET", "/page", session)).status).toBe(401);
  });

  it("is read once inside the cache window", async () => {
    let reads = 0;
    const gate: Gate = { key: "k", passwordMac: "m", bypassSecrets: [], allowOptions: [] };
    const source = gateSource(
      () => {
        reads++;
        return JSON.stringify(gate);
      },
      () => 0,
    );

    await serveAndReach(source, "GET", "/", { host: "a.example.com" });
    await serveAndReach(source, "GET", "/", { host: "a.example.com" });

    expect(reads).toBe(1);
  });
});

describe("a service whose mounted gate cannot be used", () => {
  it("serves no one when the mount cannot be read", async () => {
    const source = gateSource(
      () => new Error("ENOENT"),
      () => 0,
    );

    const reach = await serveAndReach(source, "GET", "/page", { host: "a.example.com" });

    expect([reach.status, reach.reachedApp]).toEqual([503, false]);
    expect(reach.headers["cache-control"]).toBe("private, no-store");
  });

  it("serves no one when the mount is malformed", async () => {
    const source = gateSource(
      () => "{",
      () => 0,
    );

    const reach = await serveAndReach(source, "GET", "/page", { host: "a.example.com" });

    expect([reach.status, reach.reachedApp]).toEqual([503, false]);
  });

  it("serves no one once a read that hangs passes its timeout", async () => {
    const source = newGateSource(
      () => new Promise<string>(() => {}),
      () => 0,
      20,
    );

    const reach = await serveAndReach(source, "GET", "/page", { host: "a.example.com" });

    expect([reach.status, reach.reachedApp]).toEqual([503, false]);
  });

  it("serves the gate a read that timed out finds once it finishes", async () => {
    let finish!: (contents: string) => void;
    const source = newGateSource(
      () =>
        new Promise<string>((resolve) => {
          finish = resolve;
        }),
      () => 0,
      20,
    );

    expect((await source.current()).kind).toBe("unreadable");
    finish("null");
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect((await source.current()).kind).toBe("open");
  });

  it("serves everyone once the gate is withdrawn", async () => {
    const source = gateSource(
      () => "null",
      () => 0,
    );

    const reach = await serveAndReach(source, "GET", "/page", { host: "a.example.com" });

    expect([reach.status, reach.reachedApp]).toEqual([200, true]);
  });
});

describe("an admitted response", () => {
  const gate: Gate = {
    key: "k",
    passwordMac: "m",
    bypassSecrets: ["let-me-in"],
    allowOptions: [],
  };
  const admitted = { host: "a.example.com", "x-ocel-bypass": "let-me-in" };

  it.each([
    [
      "pairs",
      [
        ["set-cookie", "a=1"],
        ["set-cookie", "b=2"],
      ],
    ],
    ["a flat list", ["set-cookie", "a=1", "set-cookie", "b=2"]],
  ])("keeps every header the app repeats when it hands writeHead %s", async (_, headers) => {
    const reach = await serveAndReach(sourceOf(gate), "GET", "/", admitted, (res) => {
      res.writeHead(200, headers as any);
      res.end();
    });

    expect(reach.reachedApp).toBe(true);
    expect(reach.headers["set-cookie"]).toEqual(["a=1", "b=2"]);
    expect(reach.headers["x-robots-tag"]).toBeDefined();
  });

  it("lets a flat list replace a header the app set before", async () => {
    const reach = await serveAndReach(sourceOf(gate), "GET", "/", admitted, (res) => {
      res.setHeader("content-type", "text/plain");
      res.writeHead(200, ["content-type", "text/html"]);
      res.end();
    });

    expect(reach.headers["content-type"]).toBe("text/html");
  });
});

describe("opening the gate a service mounts", () => {
  it("reads an inline gate when no file is named", async () => {
    const encoded = JSON.stringify({
      key: "k",
      passwordMac: await hashPassword("k", "pw"),
      bypassSecrets: [],
      allowOptions: [],
    });

    const reach = await serveAndReach(openGate({ inline: encoded }), "GET", "/", {
      host: "a.example.com",
    });

    expect(reach.status).toBe(401);
  });
});

describe("a websocket upgrade", () => {
  it("is refused with the gate's answer when it carries no credentials", async () => {
    const gate: Gate = { key: "k", passwordMac: "m", bypassSecrets: [], allowOptions: [] };
    const source = sourceOf(gate);
    let reachedApp = false;
    const server = http.createServer();
    server.on("upgrade", async (req, socket) => {
      if (!(await admitGateUpgrade(source, req, socket))) return;
      reachedApp = true;
      socket.end();
    });
    closing.push(server);
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const { port } = server.address() as AddressInfo;

    const answer = await new Promise<string>((resolve) => {
      const req = http.request({
        host: "127.0.0.1",
        port,
        path: "/socket",
        headers: { connection: "Upgrade", upgrade: "websocket", host: "a.example.com" },
      });
      req.on("response", (res) => resolve(`HTTP ${res.statusCode}`));
      req.on("upgrade", () => resolve("upgraded"));
      req.on("error", () => resolve("error"));
      req.end();
    });

    expect([answer, reachedApp]).toEqual(["HTTP 401", false]);
  });
});

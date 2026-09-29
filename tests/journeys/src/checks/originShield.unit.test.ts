import { afterAll, afterEach, beforeAll, describe, expect, it } from "bun:test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import tls from "node:tls";
import {
  askOverPlainHTTP,
  askOverTLS,
  assertRedirectedToHTTPS,
  assertRefused,
  type Outcome,
  readOriginAddress,
  statusOf,
} from "./originShield";

const realFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = realFetch;
});

function cloudflareServing(records: Record<string, unknown[]>): string[] {
  const asked: string[] = [];
  globalThis.fetch = (async (input: string | URL | Request) => {
    const url = new URL(String(input));
    asked.push(`${url.pathname}${url.search}`);
    const zone = url.searchParams.get("name");
    if (url.pathname === "/client/v4/zones") {
      return Response.json({
        success: true,
        result: zone === "example.com" ? [{ id: "zone1" }] : [],
      });
    }
    return Response.json({ success: true, result: records[zone ?? ""] ?? [] });
  }) as typeof fetch;
  return asked;
}

describe("the origin a proxied record names", () => {
  it("is read from the proxied record of the hostname, in the zone that serves it", async () => {
    cloudflareServing({
      "web.j.example.com": [
        { type: "TXT", content: "unrelated", proxied: false },
        { type: "A", content: "198.51.100.4", proxied: true },
      ],
    });
    expect(await readOriginAddress("web.j.example.com", "token")).toBe("198.51.100.4");
  });

  it("is the IPv4 address when the hostname has an AAAA record too", async () => {
    cloudflareServing({
      "web.j.example.com": [
        { type: "AAAA", content: "2001:db8::4", proxied: true },
        { type: "A", content: "198.51.100.4", proxied: true },
      ],
    });
    expect(await readOriginAddress("web.j.example.com", "token")).toBe("198.51.100.4");
  });

  it("is refused when the hostname has no proxied record", async () => {
    cloudflareServing({
      "web.j.example.com": [{ type: "A", content: "198.51.100.4", proxied: false }],
    });
    await expect(readOriginAddress("web.j.example.com", "token")).rejects.toThrow(
      /no proxied record/,
    );
  });
});

describe("the status an origin answers", () => {
  it("is read off the status line, and absent when nothing answered over HTTP", () => {
    expect(statusOf("HTTP/1.1 403 Forbidden\r\ncontent-length: 0\r\n\r\n")).toBe(403);
    expect(statusOf("")).toBeUndefined();
  });
});

const HOSTNAME = "origin.test";
let certificate = "";
let key = "";
const servers: net.Server[] = [];

beforeAll(() => {
  const dir = mkdtempSync(join(tmpdir(), "origin-shield-"));
  try {
    execFileSync(
      "openssl",
      [
        "req",
        "-x509",
        "-newkey",
        "ec",
        "-pkeyopt",
        "ec_paramgen_curve:prime256v1",
        "-nodes",
        "-keyout",
        join(dir, "key.pem"),
        "-out",
        join(dir, "cert.pem"),
        "-days",
        "1",
        "-subj",
        `/CN=${HOSTNAME}`,
        "-addext",
        `subjectAltName=DNS:${HOSTNAME}`,
      ],
      { stdio: "ignore" },
    );
    certificate = readFileSync(join(dir, "cert.pem"), "utf8");
    key = readFileSync(join(dir, "key.pem"), "utf8");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

afterAll(() => {
  for (const server of servers) server.close();
});

function listening(server: net.Server): Promise<number> {
  servers.push(server);
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => resolve((server.address() as net.AddressInfo).port));
  });
}

const ANSWER = "HTTP/1.1 200 OK\r\ncontent-length: 2\r\nconnection: close\r\n\r\nok";

function origin(port: number): {
  address: string;
  port: number;
  hostname: string;
  timeoutMs: number;
} {
  return { address: "127.0.0.1", port, hostname: HOSTNAME, timeoutMs: 1_500 };
}

function answersEveryone(): tls.Server {
  return tls.createServer({ cert: certificate, key }, (socket) => {
    socket.once("data", () => socket.end(ANSWER));
  });
}

describe("asking an origin over TLS with no client certificate", () => {
  it("is refused by an origin that requires one", async () => {
    const port = await listening(
      tls.createServer(
        { cert: certificate, key, requestCert: true, rejectUnauthorized: true },
        (socket) => {
          socket.once("data", () => socket.end(ANSWER));
        },
      ),
    );
    const outcome = await askOverTLS(origin(port));
    expect(outcome).toMatchObject({ kind: "refused" });
    expect(() => assertRefused(outcome, "the origin")).not.toThrow();
  });

  it("is answered by an origin that requires none, and the check fails", async () => {
    const port = await listening(answersEveryone());
    const outcome = await askOverTLS(origin(port));
    expect(outcome).toMatchObject({ kind: "answered", status: 200 });
    expect(() => assertRefused(outcome, "the origin")).toThrow(/answered 200/);
  });

  it("fails the check when nothing listens at the address", async () => {
    const closed = await listening(net.createServer());
    servers.pop()?.close();
    await new Promise((resolve) => setTimeout(resolve, 50));
    const outcome = await askOverTLS(origin(closed));
    expect(outcome.kind).toBe("unreachable");
    expect(() => assertRefused(outcome, "the origin")).toThrow(/never reached/);
  });

  it("fails the check when the origin connects and never speaks", async () => {
    const port = await listening(net.createServer(() => {}));
    const outcome = await askOverTLS(origin(port));
    expect(outcome.kind).toBe("undecided");
    expect(() => assertRefused(outcome, "the origin")).toThrow(/connected and then/);
  });
});

function answeringPlain(answer: string): net.Server {
  return net.createServer((socket) => {
    socket.once("data", () => socket.end(answer));
  });
}

describe("asking an origin over plain HTTP", () => {
  it("passes when it redirects to the same hostname over https", async () => {
    const port = await listening(
      answeringPlain(
        `HTTP/1.1 308 Permanent Redirect\r\nLocation: https://${HOSTNAME}/\r\ncontent-length: 0\r\n\r\n`,
      ),
    );
    const outcome = await askOverPlainHTTP(origin(port));
    expect(outcome).toMatchObject({
      kind: "answered",
      status: 308,
      location: `https://${HOSTNAME}/`,
    });
    expect(() => assertRedirectedToHTTPS(outcome, "the origin", HOSTNAME)).not.toThrow();
  });

  for (const [what, answer] of [
    ["forwards it to the app", ANSWER],
    ["refuses it", "HTTP/1.1 403 Forbidden\r\ncontent-length: 0\r\n\r\n"],
    [
      "redirects it elsewhere",
      "HTTP/1.1 308 Permanent Redirect\r\nLocation: https://elsewhere.test/\r\n\r\n",
    ],
  ] as const) {
    it(`fails when it ${what}`, async () => {
      const port = await listening(answeringPlain(answer));
      const outcome: Outcome = await askOverPlainHTTP(origin(port));
      expect(() => assertRedirectedToHTTPS(outcome, "the origin", HOSTNAME)).toThrow(/want 308/);
    });
  }

  it("fails when nothing listens on port 80", async () => {
    const closed = await listening(net.createServer());
    servers.pop()?.close();
    await new Promise((resolve) => setTimeout(resolve, 50));
    const outcome = await askOverPlainHTTP(origin(closed));
    expect(outcome.kind).toBe("unreachable");
    expect(() => assertRedirectedToHTTPS(outcome, "the origin", HOSTNAME)).toThrow(/never reached/);
  });
});

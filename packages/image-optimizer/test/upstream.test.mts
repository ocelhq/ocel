import http from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, describe, expect, test } from "vitest";
import { isReachableAddress } from "../src/addresses.mjs";
import { fetchUpstream, guardedLookup, type UpstreamDeps } from "../src/upstream.mjs";
import { imageConfig } from "./fixtures.mjs";
import { solid } from "./images.mjs";

// The SSRF surface, tested against real sockets and a real DNS lookup seam.
//
// Every test that wants to be served has to inject a policy that admits
// loopback, because the production policy does not — which is itself asserted
// below. Nothing here mocks undici: the point of the pattern is where in the
// connection the check sits, and a mocked dispatcher would move it.

const servers: http.Server[] = [];

afterEach(async () => {
  await Promise.all(
    servers.splice(0).map(
      (server) => new Promise<void>((resolve) => server.close(() => resolve())),
    ),
  );
});

interface Served {
  port: number;
  requests: http.IncomingMessage[];
}

function serve(handler: http.RequestListener): Promise<Served> {
  const requests: http.IncomingMessage[] = [];
  const server = http.createServer((req, res) => {
    requests.push(req);
    handler(req, res);
  });
  servers.push(server);
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () =>
      resolve({ port: (server.address() as AddressInfo).port, requests }),
    );
  });
}

// A policy that treats loopback as reachable and everything the real policy
// denies as denied, so a test can be served without the rest of the guard being
// switched off.
const allowLoopback = (address: string) =>
  address === "127.0.0.1" || isReachableAddress(address);

function resolvesTo(...addresses: string[]): UpstreamDeps["lookup"] {
  return ((_hostname: string, _options: unknown, callback: Function) => {
    callback(
      null,
      addresses.map((address) => ({ address, family: address.includes(":") ? 6 : 4 })),
    );
  }) as UpstreamDeps["lookup"];
}

const loopback: UpstreamDeps = { lookup: resolvesTo("127.0.0.1"), isReachable: allowLoopback };

function config(overrides = {}) {
  return imageConfig({
    remotePatterns: [{ protocol: "http", hostname: "^cdn\\.test$", pathname: "^\\/.*$" }],
    ...overrides,
  });
}

describe("the happy path", () => {
  test("fetches the bytes and relays the upstream directives", async () => {
    const image = await solid("jpeg");
    const { port } = await serve((_req, res) => {
      res.writeHead(200, {
        "content-type": "image/jpeg",
        "cache-control": "public, s-maxage=3600, max-age=60",
        etag: '"abc123"',
      });
      res.end(Buffer.from(image));
    });
    const result = await fetchUpstream(`http://cdn.test:${port}/x.jpg`, config(), loopback);
    expect(Buffer.from(result.bytes).equals(Buffer.from(image))).toBe(true);
    // Verbatim: the edge never talks to this server, so this header is the only
    // path by which its freshness reaches the tier that caches on it.
    expect(result.cacheControl).toBe("public, s-maxage=3600, max-age=60");
    expect(result.etag).toBe('"abc123"');
  });

  // Divergence 3, and the reason Content-Length can be trusted as a pre-check:
  // there is no encoding layer between the sender's byte count and ours.
  test("asks for identity encoding and sends no client-derived header", async () => {
    const { port, requests } = await serve((_req, res) => {
      res.writeHead(200, { "content-type": "image/jpeg" });
      res.end(Buffer.from([0xff, 0xd8, 0xff, 0x00]));
    });
    await fetchUpstream(`http://cdn.test:${port}/x.jpg`, config(), loopback);
    const headers = requests[0]!.headers;
    expect(headers["accept-encoding"]).toBe("identity");
    expect(headers["accept"]).toBe("image/*");
    // CVE-2025-57752: a forwarded Cookie plus a cache key with no header
    // component served one user's private image to everyone. There is no path
    // from a client header to this request at all — fetchUpstream is not even
    // given one.
    expect(headers["cookie"]).toBeUndefined();
    expect(headers["authorization"]).toBeUndefined();
    // The whole request, exhaustively: the two headers this function sets, plus
    // the two the transport owns. There is nowhere else for a header to enter.
    expect(Object.keys(headers).sort()).toEqual([
      "accept",
      "accept-encoding",
      "connection",
      "host",
    ]);
  });
});

describe("the address policy at connect time", () => {
  // No injected policy: the production one, against a server that really is on
  // loopback and a hostname that really resolves there.
  test("the real policy refuses a name that resolves to loopback", async () => {
    const { port, requests } = await serve((_req, res) => {
      res.writeHead(200, { "content-type": "image/jpeg" });
      res.end(Buffer.from([0xff, 0xd8, 0xff, 0x00]));
    });
    await expect(
      fetchUpstream(
        `http://localhost:${port}/x.jpg`,
        config({
          remotePatterns: [
            { protocol: "http", hostname: "^localhost$", pathname: "^\\/.*$" },
          ],
        }),
      ),
    ).rejects.toThrow('"url" parameter is valid but upstream response is invalid');
    // Never connected. The refusal is inside the lookup the socket uses, so
    // there is no request for the server to have seen.
    expect(requests).toEqual([]);
  });

  // Divergence 2. Next resolves, checks, then calls fetch(hostname), which
  // re-resolves — the window a rebinding DNS server aims at. Here the check is
  // the resolution the socket uses, so the second answer is checked as freshly
  // as the first.
  test("a second resolution is validated as freshly as the first", async () => {
    const { port } = await serve((_req, res) => {
      res.writeHead(200, { "content-type": "image/jpeg" });
      res.end(Buffer.from([0xff, 0xd8, 0xff, 0x00]));
    });

    let calls = 0;
    const rebinding: UpstreamDeps = {
      // First 127.0.0.1, which this policy admits; then the instance metadata
      // service, which it does not.
      lookup: ((_hostname: string, _options: unknown, callback: Function) => {
        calls += 1;
        const address = calls === 1 ? "127.0.0.1" : "169.254.169.254";
        callback(null, [{ address, family: 4 }]);
      }) as UpstreamDeps["lookup"],
      isReachable: allowLoopback,
    };

    const href = `http://cdn.test:${port}/x.jpg`;
    await expect(fetchUpstream(href, config(), rebinding)).resolves.toBeTruthy();
    await expect(fetchUpstream(href, config(), rebinding)).rejects.toThrow(
      '"url" parameter is valid but upstream response is invalid',
    );
    expect(calls).toBe(2);
  });

  test("a name whose every address is denied never opens a socket", async () => {
    await expect(
      fetchUpstream("http://cdn.test/x.jpg", config(), {
        lookup: resolvesTo("169.254.169.254", "10.0.0.1", "::1"),
        isReachable: isReachableAddress,
      }),
    ).rejects.toThrow('"url" parameter is valid but upstream response is invalid');
  });
});

// The most commonly botched part of the pattern. Node 20+ turns on
// autoSelectFamily, which forces all:true, so a lookup that answers with a
// single address — or that filters only the first entry — either breaks
// connections or leaks the entries it did not look at.
describe("the lookup's all contract", () => {
  const deps: UpstreamDeps = {
    lookup: resolvesTo("10.0.0.1", "76.76.21.21", "169.254.169.254", "8.8.8.8"),
    isReachable: isReachableAddress,
  };

  test("all:true returns the whole filtered list, in order", async () => {
    const result = await new Promise<unknown>((resolve) =>
      guardedLookup(deps)("cdn.test", { all: true } as never, ((
        _err: unknown,
        value: unknown,
      ) => resolve(value)) as never),
    );
    expect(result).toEqual([
      { address: "76.76.21.21", family: 4 },
      { address: "8.8.8.8", family: 4 },
    ]);
  });

  test("all:false returns a single address, not an array", async () => {
    const result = await new Promise<unknown[]>((resolve) =>
      guardedLookup(deps)("cdn.test", {} as never, ((...args: unknown[]) =>
        resolve(args)) as never),
    );
    expect(result).toEqual([null, "76.76.21.21", 4]);
  });

  test("an empty filtered list is an error, never an empty success", async () => {
    const blocked: UpstreamDeps = {
      lookup: resolvesTo("10.0.0.1", "127.0.0.1"),
      isReachable: isReachableAddress,
    };
    const error = await new Promise<unknown>((resolve) =>
      guardedLookup(blocked)("cdn.test", { all: true } as never, ((
        err: unknown,
      ) => resolve(err)) as never),
    );
    expect(error).toBeInstanceOf(Error);
  });

  test("a resolver failure is relayed rather than swallowed", async () => {
    const failing: UpstreamDeps = {
      lookup: ((_h: string, _o: unknown, cb: Function) =>
        cb(new Error("ENOTFOUND"))) as UpstreamDeps["lookup"],
      isReachable: allowLoopback,
    };
    const error = await new Promise<unknown>((resolve) =>
      guardedLookup(failing)("cdn.test", { all: true } as never, ((
        err: unknown,
      ) => resolve(err)) as never),
    );
    expect((error as Error).message).toBe("ENOTFOUND");
  });
});

describe("redirects", () => {
  test("are followed by hand, and the body of every hop is drained", async () => {
    const image = await solid("jpeg");
    const { port, requests } = await serve((req, res) => {
      if (req.url === "/start") {
        res.writeHead(302, { location: "/final", "content-type": "text/html" });
        res.end("<html>ignored</html>");
        return;
      }
      res.writeHead(200, { "content-type": "image/jpeg" });
      res.end(Buffer.from(image));
    });
    const result = await fetchUpstream(`http://cdn.test:${port}/start`, config(), loopback);
    expect(Buffer.from(result.bytes).equals(Buffer.from(image))).toBe(true);
    expect(requests.map((r) => r.url)).toEqual(["/start", "/final"]);
  });

  // Divergence 1. Next says redirects "do not need to satisfy remotePatterns";
  // on an optimizer shared by every app in an account, that turns an open
  // redirect on any one tenant's allowlisted CDN into a fetch primitive aimed at
  // everyone's.
  test("a hop outside the allowlist is refused", async () => {
    const { port } = await serve((_req, res) => {
      res.writeHead(302, { location: "http://evil.test/x.jpg" });
      res.end();
    });
    await expect(
      fetchUpstream(`http://cdn.test:${port}/start`, config(), loopback),
    ).rejects.toThrow('"url" parameter is valid but upstream response is invalid');
  });

  test("a hop to a non-http scheme is refused", async () => {
    const { port } = await serve((_req, res) => {
      res.writeHead(302, { location: "file:///etc/passwd" });
      res.end();
    });
    await expect(
      fetchUpstream(`http://cdn.test:${port}/start`, config(), loopback),
    ).rejects.toThrow('"url" parameter is valid but upstream response is invalid');
  });

  // The IP re-check per hop comes free from the agent, because every hop goes
  // through the same guarded lookup. Asserted rather than assumed: a redirect to
  // an allowlisted name that resolves somewhere denied must not be followed.
  test("a hop whose host resolves somewhere denied is refused", async () => {
    let calls = 0;
    const { port } = await serve((_req, res) => {
      res.writeHead(302, { location: `http://cdn.test:${port}/final` });
      res.end();
    });
    const rebinding: UpstreamDeps = {
      lookup: ((_h: string, _o: unknown, cb: Function) => {
        calls += 1;
        cb(null, [{ address: calls === 1 ? "127.0.0.1" : "169.254.169.254", family: 4 }]);
      }) as UpstreamDeps["lookup"],
      isReachable: allowLoopback,
    };
    await expect(
      fetchUpstream(`http://cdn.test:${port}/start`, config(), rebinding),
    ).rejects.toThrow('"url" parameter is valid but upstream response is invalid');
    expect(calls).toBe(2);
  });

  test("stop at three hops", async () => {
    const { port, requests } = await serve((_req, res) => {
      res.writeHead(302, { location: "/again" });
      res.end();
    });
    await expect(
      fetchUpstream(`http://cdn.test:${port}/start`, config(), loopback),
    ).rejects.toThrow('"url" parameter is valid but upstream response is invalid');
    // The first request plus three hops, and then no more.
    expect(requests.length).toBe(4);
  });

  test("a lower maximumRedirects in the config wins", async () => {
    const { port, requests } = await serve((_req, res) => {
      res.writeHead(302, { location: "/again" });
      res.end();
    });
    await expect(
      fetchUpstream(
        `http://cdn.test:${port}/start`,
        config({ maximumRedirects: 1 }),
        loopback,
      ),
    ).rejects.toThrow();
    expect(requests.length).toBe(2);
  });
});

describe("the byte ceiling", () => {
  test("aborts a body that grows past it, with no Content-Length to warn us", async () => {
    const { port } = await serve((_req, res) => {
      res.writeHead(200, { "content-type": "image/jpeg" });
      // Chunked: nothing declares a length, so the only thing that can stop this
      // is counting the bytes as they arrive.
      for (let i = 0; i < 64; i++) res.write(Buffer.alloc(64 * 1024));
      res.end();
    });
    await expect(
      fetchUpstream(
        `http://cdn.test:${port}/x.jpg`,
        config({ maximumResponseBody: 128 * 1024 }),
        loopback,
      ),
    ).rejects.toThrow('"url" parameter is valid but upstream response is invalid');
  });

  test("refuses a declared length over it before reading a byte of body", async () => {
    let bodyWritten = false;
    const { port } = await serve((_req, res) => {
      res.writeHead(200, {
        "content-type": "image/jpeg",
        "content-length": String(4 * 1024 * 1024),
      });
      res.on("close", () => {
        bodyWritten = true;
      });
      res.write(Buffer.alloc(1024));
    });
    await expect(
      fetchUpstream(
        `http://cdn.test:${port}/x.jpg`,
        config({ maximumResponseBody: 1024 }),
        loopback,
      ),
    ).rejects.toThrow('"url" parameter is valid but upstream response is invalid');
    expect(bodyWritten).toBe(true);
  });

  test("a body exactly at the ceiling is served", async () => {
    const image = await solid("jpeg");
    const { port } = await serve((_req, res) => {
      res.writeHead(200, { "content-type": "image/jpeg" });
      res.end(Buffer.from(image));
    });
    const result = await fetchUpstream(
      `http://cdn.test:${port}/x.jpg`,
      config({ maximumResponseBody: image.byteLength }),
      loopback,
    );
    expect(result.bytes.byteLength).toBe(image.byteLength);
  });
});

describe("upstream failures", () => {
  for (const status of [301, 404, 403, 500, 503]) {
    test(`a ${status} with no usable Location is one generic answer`, async () => {
      const { port } = await serve((_req, res) => {
        res.writeHead(status);
        res.end("detail the caller must not learn");
      });
      await expect(
        fetchUpstream(`http://cdn.test:${port}/x.jpg`, config(), loopback),
      ).rejects.toThrow('"url" parameter is valid but upstream response is invalid');
    });
  }

  // One status and one string for every cause. Next answers 504 for a timeout,
  // 508 for a redirect loop, 413 for an oversized body and the upstream's own
  // status otherwise — each of which is an oracle bit about a network the caller
  // cannot otherwise see.
  test("a connection refused is the same answer as everything else", async () => {
    const { port } = await serve((_req, res) => res.end());
    await new Promise<void>((resolve) => servers[0]!.close(() => resolve()));
    servers.length = 0;
    await expect(
      fetchUpstream(`http://cdn.test:${port}/x.jpg`, config(), loopback),
    ).rejects.toThrow('"url" parameter is valid but upstream response is invalid');
  });
});

// The wall clock, and why a byte ceiling is not a substitute for it. This server
// stays under every inactivity timeout by writing a byte every 100 ms and stays
// under the byte ceiling forever; only an independent deadline ends it.
test(
  "a slowloris origin is ended by the wall-clock deadline",
  async () => {
    const timers: NodeJS.Timeout[] = [];
    const { port } = await serve((_req, res) => {
      res.writeHead(200, { "content-type": "image/jpeg" });
      res.write(Buffer.from([0xff, 0xd8, 0xff]));
      timers.push(
        setInterval(() => {
          res.write(Buffer.from([0x00]));
        }, 100),
      );
    });
    const started = Date.now();
    try {
      await expect(
        fetchUpstream(`http://cdn.test:${port}/x.jpg`, config(), loopback),
      ).rejects.toThrow('"url" parameter is valid but upstream response is invalid');
    } finally {
      timers.forEach((timer) => clearInterval(timer));
    }
    // It ended, and it ended on the deadline rather than on a byte count that
    // would never have been reached.
    expect(Date.now() - started).toBeGreaterThan(5_000);
  },
  20_000,
);

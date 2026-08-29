import http from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, describe, expect, test } from "vitest";
import { isReachableAddress } from "../src/addresses.mjs";
import { fetchUpstream, guardedLookup, type UpstreamDeps } from "../src/upstream.mjs";
import { imageConfig } from "./fixtures.mjs";
import { solid } from "./images.mjs";

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

function serveJpeg(): Promise<Served> {
  return serve((_req, res) => {
    res.writeHead(200, { "content-type": "image/jpeg" });
    res.end(Buffer.from([0xff, 0xd8, 0xff, 0x00]));
  });
}

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
    expect(result.cacheControl).toBe("public, s-maxage=3600, max-age=60");
    expect(result.etag).toBe('"abc123"');
  });

  test("asks for identity encoding and sends no client-derived header", async () => {
    const { port, requests } = await serve((_req, res) => {
      res.writeHead(200, { "content-type": "image/jpeg" });
      res.end(Buffer.from([0xff, 0xd8, 0xff, 0x00]));
    });
    await fetchUpstream(`http://cdn.test:${port}/x.jpg`, config(), loopback);
    const headers = requests[0]!.headers;
    expect(headers["accept-encoding"]).toBe("identity");
    expect(headers["accept"]).toBe("image/*");
    expect(headers["cookie"]).toBeUndefined();
    expect(headers["authorization"]).toBeUndefined();
    expect(Object.keys(headers).sort()).toEqual([
      "accept",
      "accept-encoding",
      "connection",
      "host",
    ]);
  });
});

describe("a href the URL parser rejects", () => {
  test("is an upstream failure, never a raw TypeError", async () => {
    await expect(fetchUpstream("http://in valid/x.jpg", config())).rejects.toThrow(
      '"url" parameter is valid but upstream response is invalid',
    );
  });
});

describe("the address policy at connect time", () => {
  test("a literal loopback target never opens a socket", async () => {
    const { port, requests } = await serveJpeg();
    await expect(
      fetchUpstream(
        `http://127.0.0.1:${port}/x.jpg`,
        config({
          remotePatterns: [
            { protocol: "http", hostname: "^127\\.0\\.0\\.1$", pathname: "^\\/.*$" },
          ],
        }),
      ),
    ).rejects.toThrow('"url" parameter is valid but upstream response is invalid');
    expect(requests).toEqual([]);
  });

  for (const [literal, normalized] of [
    ["127.0.0.1", "127.0.0.1"],
    ["[::1]", "[::1]"],
    ["[::ffff:127.0.0.1]", "[::ffff:7f00:1]"],
    ["0x7f000001", "127.0.0.1"],
    ["2130706433", "127.0.0.1"],
    ["0177.0.0.1", "127.0.0.1"],
  ]) {
    test(`${literal} is checked as ${normalized} before connecting`, async () => {
      const checked: string[] = [];
      await expect(
        fetchUpstream(`http://${literal}/x.jpg`, config(), {
          isReachable: (address) => {
            checked.push(address);
            return false;
          },
        }),
      ).rejects.toThrow('"url" parameter is valid but upstream response is invalid');
      expect(checked).toEqual([normalized]);
    });
  }

  test("dangerouslyAllowLocalIP permits a literal loopback target", async () => {
    const { port, requests } = await serveJpeg();
    await expect(
      fetchUpstream(
        `http://127.0.0.1:${port}/x.jpg`,
        config({ dangerouslyAllowLocalIP: true }),
      ),
    ).resolves.toBeTruthy();
    expect(requests).toHaveLength(1);
  });

  test("dangerouslyAllowLocalIP permits a name that resolves to loopback", async () => {
    const { port, requests } = await serveJpeg();
    await expect(
      fetchUpstream(
        `http://cdn.test:${port}/x.jpg`,
        config({ dangerouslyAllowLocalIP: true }),
        { lookup: resolvesTo("127.0.0.1") },
      ),
    ).resolves.toBeTruthy();
    expect(requests).toHaveLength(1);
  });

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
    expect(requests).toEqual([]);
  });

  test("a second resolution is validated as freshly as the first", async () => {
    const { port } = await serve((_req, res) => {
      res.writeHead(200, { "content-type": "image/jpeg" });
      res.end(Buffer.from([0xff, 0xd8, 0xff, 0x00]));
    });

    let calls = 0;
    const rebinding: UpstreamDeps = {
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

  test("a numeric options argument keeps the family it names", async () => {
    let seen: unknown;
    const constrained: UpstreamDeps = {
      lookup: ((_h: string, options: unknown, cb: Function) => {
        seen = options;
        cb(null, [{ address: "8.8.8.8", family: 4 }]);
      }) as UpstreamDeps["lookup"],
      isReachable: isReachableAddress,
    };
    const result = await new Promise<unknown[]>((resolve) =>
      guardedLookup(constrained)("cdn.test", 4 as never, ((...args: unknown[]) =>
        resolve(args)) as never),
    );
    expect(seen).toMatchObject({ family: 4, all: true });
    expect(result).toEqual([null, "8.8.8.8", 4]);
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
  test("a hop to a literal loopback target never opens a second socket", async () => {
    let checks = 0;
    const { port, requests } = await serve((req, res) => {
      if (req.url === "/start") {
        res.writeHead(302, { location: `http://127.0.0.1:${port}/final` });
        res.end();
        return;
      }
      res.writeHead(200, { "content-type": "image/jpeg" });
      res.end(Buffer.from([0xff, 0xd8, 0xff, 0x00]));
    });
    await expect(
      fetchUpstream(
        `http://127.0.0.1:${port}/start`,
        config({
          remotePatterns: [
            { protocol: "http", hostname: "^127\\.0\\.0\\.1$", pathname: "^\\/.*$" },
          ],
        }),
        { isReachable: () => checks++ === 0 },
      ),
    ).rejects.toThrow('"url" parameter is valid but upstream response is invalid');
    expect(requests.map((request) => request.url)).toEqual(["/start"]);
  });

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

  test("a connection refused is the same answer as everything else", async () => {
    const { port } = await serve((_req, res) => res.end());
    await new Promise<void>((resolve) => servers[0]!.close(() => resolve()));
    servers.length = 0;
    await expect(
      fetchUpstream(`http://cdn.test:${port}/x.jpg`, config(), loopback),
    ).rejects.toThrow('"url" parameter is valid but upstream response is invalid');
  });
});

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
    expect(Date.now() - started).toBeGreaterThan(5_000);
  },
  20_000,
);

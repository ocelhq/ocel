import { afterAll, beforeAll, describe, it } from "bun:test";
import assert from "node:assert/strict";
import dgram from "node:dgram";
import { createServer, type Server } from "node:http";
import {
  type AuthoritativeResolver,
  authoritativeLookup,
  emulatorAddress,
  emulatorFetch,
  type FallbackLookup,
  type LookupAnswer,
  lookupVia,
} from "./dispatch";

describe("emulatorAddress", () => {
  it("reads the port the endpoint publishes", () => {
    assert.deepEqual(emulatorAddress("http://127.0.0.1:4566"), {
      hostname: "127.0.0.1",
      port: 4566,
    });
  });

  it("falls back to the scheme's port", () => {
    assert.deepEqual(emulatorAddress("http://floci.internal"), {
      hostname: "floci.internal",
      port: 80,
    });
  });

  it("refuses a scheme it would not dial", () => {
    assert.throws(() => emulatorAddress("https://floci.internal"), /not plain HTTP/);
  });

  it("refuses something that is not a URL", () => {
    assert.throws(() => emulatorAddress("127.0.0.1:4566"), /not a URL/);
  });
});

describe("emulatorFetch", () => {
  let server: Server;
  let endpoint: string;

  beforeAll(async () => {
    server = createServer((req, res) => {
      let body = "";
      req.on("data", (chunk) => {
        body += String(chunk);
      });
      req.on("end", () => {
        res.setHeader("content-type", "application/json");
        res.end(
          JSON.stringify({
            host: req.headers.host,
            probe: req.headers["x-ocel-probe"] ?? null,
            url: req.url,
            method: req.method,
            body,
          }),
        );
      });
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const address = server.address();
    assert.ok(address && typeof address === "object");
    endpoint = `http://127.0.0.1:${address.port}`;
  });

  afterAll(async () => {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  });

  it("dials the emulator for a hostname nothing resolves, keeping the request intact", async () => {
    const dispatch = emulatorFetch(endpoint);
    const res = await dispatch("https://web-j-1-node.journey.test/api/probes/echo?one=1", {
      method: "POST",
      headers: { "x-ocel-probe": "probe-value" },
      body: "payload",
    });
    assert.equal(res.status, 200);
    assert.deepEqual(await res.json(), {
      host: "web-j-1-node.journey.test",
      probe: "probe-value",
      url: "/api/probes/echo?one=1",
      method: "POST",
      body: "payload",
    });
  });
});

describe("authoritativeLookup", () => {
  function labels(name: string): Buffer {
    return Buffer.concat([
      ...name
        .split(".")
        .map((label) => Buffer.concat([Buffer.from([label.length]), Buffer.from(label)])),
      Buffer.from([0]),
    ]);
  }

  function record(name: string, type: number, ttl: number, data: Buffer): Buffer {
    const fixed = Buffer.alloc(10);
    fixed.writeUInt16BE(type, 0);
    fixed.writeUInt16BE(1, 2);
    fixed.writeUInt32BE(ttl, 4);
    fixed.writeUInt16BE(data.length, 8);
    return Buffer.concat([labels(name), fixed, data]);
  }

  function reply(query: Buffer, answer: "absent" | "present"): Buffer {
    let end = 12;
    const asked: string[] = [];
    while (query[end] !== 0) {
      const length = query[end] ?? 0;
      asked.push(query.subarray(end + 1, end + 1 + length).toString());
      end += length + 1;
    }
    const type = query.readUInt16BE(end + 1);
    const header = Buffer.alloc(12);
    header.writeUInt16BE(query.readUInt16BE(0), 0);
    header.writeUInt16BE(answer === "absent" ? 0x8403 : 0x8400, 2);
    header.writeUInt16BE(1, 4);
    const question = query.subarray(12, end + 5);
    if (answer === "present" && type === 1) {
      header.writeUInt16BE(1, 6);
      return Buffer.concat([
        header,
        question,
        record(asked.join("."), 1, 300, Buffer.from([198, 51, 100, 4])),
      ]);
    }
    header.writeUInt16BE(1, 8);
    const soa = Buffer.concat([
      labels("ns.ocel.site"),
      labels("hostmaster.ocel.site"),
      Buffer.from([0, 0, 0, 1, 0, 0, 14, 16, 0, 0, 14, 16, 0, 0, 14, 16, 0, 0, 7, 8]),
    ]);
    return Buffer.concat([header, question, record("ocel.site", 6, 1800, soa)]);
  }

  it("asks the zone again after a miss rather than replaying the zone's negative ttl", async () => {
    let written = false;
    const socket = dgram.createSocket("udp4");
    socket.on("message", (query, peer) => {
      socket.send(reply(query, written ? "present" : "absent"), peer.port, peer.address);
    });
    await new Promise<void>((resolve) => socket.bind(0, "127.0.0.1", resolve));
    const lookup = authoritativeLookup([`127.0.0.1:${socket.address().port}`], async () => []);
    const ask = () =>
      new Promise<NodeJS.ErrnoException | string | undefined>((resolve) => {
        lookup("web-j-1.ocel.site", {}, (error, address) =>
          resolve(error ?? (address as string | undefined)),
        );
      });
    try {
      assert.equal(((await ask()) as NodeJS.ErrnoException).code, "ENOTFOUND");
      written = true;
      assert.equal(await ask(), "198.51.100.4");
    } finally {
      socket.close();
    }
  });
});

describe("lookupVia", () => {
  type LookupResult =
    | { error: NodeJS.ErrnoException }
    | { address: string | LookupAnswer[] | undefined; family: number | undefined };

  function refused(code: string): NodeJS.ErrnoException {
    const error: NodeJS.ErrnoException = new Error(`the authority said ${code}`);
    error.code = code;
    return error;
  }

  function answered(
    resolver: AuthoritativeResolver,
    fallback: FallbackLookup,
    hostname: string,
    options: { all?: boolean } = {},
  ): Promise<LookupResult> {
    return new Promise((resolve) => {
      lookupVia(resolver, fallback)(hostname, options, (error, address, family) => {
        resolve(error ? { error } : { address, family });
      });
    });
  }

  it("hands a CNAME target to the fallback lookup, not to the authority", async () => {
    const asked: string[] = [];
    const result = await answered(
      {
        resolveCname: async () => ["dualstack.elb.amazonaws.com"],
        resolve4: async () => {
          throw new Error("the authority was asked about a name it does not serve");
        },
      },
      async (target) => {
        asked.push(target);
        return ["203.0.113.7", "203.0.113.8"];
      },
      "web-j-1.ocel.site",
    );
    assert.deepEqual(asked, ["dualstack.elb.amazonaws.com"]);
    assert.deepEqual(result, { address: "203.0.113.7", family: 4 });
  });

  it("names the CNAME target when the fallback has no address for it", async () => {
    const result = await answered(
      {
        resolveCname: async () => ["d-abc123.execute-api.us-east-1.amazonaws.com"],
        resolve4: async () => {
          throw new Error("the authority was asked about a name it does not serve");
        },
      },
      async () => [],
      "web-j-1.ocel.site",
    );
    assert.match(
      "error" in result ? result.error.message : "",
      /d-abc123\.execute-api\.us-east-1\.amazonaws\.com/,
    );
  });

  it("answers from the authority's own A record when no CNAME exists", async () => {
    const result = await answered(
      {
        resolveCname: async () => {
          throw refused("ENODATA");
        },
        resolve4: async () => ["198.51.100.4", "198.51.100.5"],
      },
      async () => {
        throw new Error("the fallback was asked about a name that has no CNAME");
      },
      "web-j-1.ocel.site",
    );
    assert.deepEqual(result, { address: "198.51.100.4", family: 4 });
  });

  it("returns every address in the array form when all is set", async () => {
    const result = await answered(
      {
        resolveCname: async () => [],
        resolve4: async () => ["198.51.100.4", "198.51.100.5"],
      },
      async () => {
        throw new Error("the fallback was asked about a name that has no CNAME");
      },
      "web-j-1.ocel.site",
      { all: true },
    );
    assert.deepEqual(result, {
      address: [
        { address: "198.51.100.4", family: 4 },
        { address: "198.51.100.5", family: 4 },
      ],
      family: undefined,
    });
  });

  it("hands an authority failure of any other kind to the callback", async () => {
    const result = await answered(
      {
        resolveCname: async () => {
          throw refused("ESERVFAIL");
        },
        resolve4: async () => ["198.51.100.4"],
      },
      async () => {
        throw new Error("the fallback was asked about a name that never resolved");
      },
      "web-j-1.ocel.site",
    );
    assert.deepEqual(result, { error: refused("ESERVFAIL") });
  });
});

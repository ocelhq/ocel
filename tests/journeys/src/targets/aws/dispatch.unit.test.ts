import assert from "node:assert/strict";
import { createServer, type Server } from "node:http";
import { afterAll, beforeAll, describe, it } from "bun:test";
import {
  type AuthoritativeResolver,
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
    const res = await dispatch("https://web-j-1-express.journey.test/api/probes/echo?one=1", {
      method: "POST",
      headers: { "x-ocel-probe": "probe-value" },
      body: "payload",
    });
    assert.equal(res.status, 200);
    assert.deepEqual(await res.json(), {
      host: "web-j-1-express.journey.test",
      probe: "probe-value",
      url: "/api/probes/echo?one=1",
      method: "POST",
      body: "payload",
    });
  });
});

describe("lookupVia", () => {
  type Settled =
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
  ): Promise<Settled> {
    return new Promise((resolve) => {
      lookupVia(resolver, fallback)(hostname, options, (error, address, family) => {
        resolve(error ? { error } : { address, family });
      });
    });
  }

  it("hands a CNAME target to the fallback lookup, not to the authority", async () => {
    const asked: string[] = [];
    const settled = await answered(
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
    assert.deepEqual(settled, { address: "203.0.113.7", family: 4 });
  });

  it("names the CNAME target when the fallback has no address for it", async () => {
    const settled = await answered(
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
      "error" in settled ? settled.error.message : "",
      /d-abc123\.execute-api\.us-east-1\.amazonaws\.com/,
    );
  });

  it("answers from the authority's own A record when no CNAME stands", async () => {
    const settled = await answered(
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
    assert.deepEqual(settled, { address: "198.51.100.4", family: 4 });
  });

  it("returns every address in the array form when all is set", async () => {
    const settled = await answered(
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
    assert.deepEqual(settled, {
      address: [
        { address: "198.51.100.4", family: 4 },
        { address: "198.51.100.5", family: 4 },
      ],
      family: undefined,
    });
  });

  it("carries an authority that fails for any other reason to the callback", async () => {
    const settled = await answered(
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
    assert.deepEqual(settled, { error: refused("ESERVFAIL") });
  });
});

import assert from "node:assert/strict";
import { createServer, type Server } from "node:http";
import { afterAll, beforeAll, describe, it } from "vitest";
import { emulatorAddress, emulatorFetch } from "./dispatch";

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
    assert.deepEqual(emulatorAddress("https://floci.internal"), {
      hostname: "floci.internal",
      port: 443,
    });
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
    const res = await dispatch("https://web.j-1-express.journey.test/api/probes/echo?one=1", {
      method: "POST",
      headers: { "x-ocel-probe": "probe-value" },
      body: "payload",
    });
    assert.equal(res.status, 200);
    assert.deepEqual(await res.json(), {
      host: "web.j-1-express.journey.test",
      probe: "probe-value",
      url: "/api/probes/echo?one=1",
      method: "POST",
      body: "payload",
    });
  });
});

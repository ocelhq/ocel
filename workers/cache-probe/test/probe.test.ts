import { SELF } from "cloudflare:test";
import { describe, expect, it } from "vitest";

// These assert the probe's HTTP contract — the shape the runner parses — and
// nothing about caching behaviour. Miniflare's cache is a local in-process
// store with one isolate and no colo, so a hit here is evidence the probe wires
// up, never evidence about Cloudflare's edge.

const url = (path: string) => `https://probe.test${path}`;

describe("/identity", () => {
  it("reports a stable isolate id and the host", async () => {
    const first = await (await SELF.fetch(url("/identity"))).json<{
      isolate: string;
      host: string;
    }>();
    const second = await (await SELF.fetch(url("/identity"))).json<{ isolate: string }>();

    expect(first.isolate).toMatch(/^[0-9a-f]{8}$/);
    expect(second.isolate).toBe(first.isolate);
    expect(first.host).toBe("probe.test");
  });
});

describe("/entry", () => {
  it("requires a run id", async () => {
    expect((await SELF.fetch(url("/entry"))).status).toBe(400);
  });

  it("rejects a non-positive ttl", async () => {
    const response = await SELF.fetch(url("/entry?run=bad-ttl&ttl=0"), { method: "PUT" });
    expect(response.status).toBe(400);
  });

  it("reads back the writing isolate and the requested ttl", async () => {
    const written = await (
      await SELF.fetch(url("/entry?run=readback&ttl=10"), { method: "PUT" })
    ).json<{ isolate: string; sentinel: { ttlSeconds: number } }>();

    const read = await (await SELF.fetch(url("/entry?run=readback"))).json<{
      hit: boolean;
      writer: string | null;
      requestedTtlSeconds: number | null;
    }>();

    expect(written.sentinel.ttlSeconds).toBe(10);
    expect(read.hit).toBe(true);
    expect(read.writer).toBe(written.isolate);
    expect(read.requestedTtlSeconds).toBe(10);
  });

  it("reports a miss for a run nothing wrote", async () => {
    const read = await (await SELF.fetch(url("/entry?run=never-written"))).json<{
      hit: boolean;
      writer: string | null;
    }>();

    expect(read).toMatchObject({ hit: false, writer: null });
  });

  it("keys entries per run", async () => {
    await SELF.fetch(url("/entry?run=alpha&ttl=30"), { method: "PUT" });
    const read = await (await SELF.fetch(url("/entry?run=beta"))).json<{ hit: boolean }>();

    expect(read.hit).toBe(false);
  });

  it("deletes an entry", async () => {
    await SELF.fetch(url("/entry?run=deletable&ttl=30"), { method: "PUT" });
    const deleted = await (
      await SELF.fetch(url("/entry?run=deletable"), { method: "DELETE" })
    ).json<{ deleted: boolean }>();
    const read = await (await SELF.fetch(url("/entry?run=deletable"))).json<{
      hit: boolean;
    }>();

    expect(deleted.deleted).toBe(true);
    expect(read.hit).toBe(false);
  });
});

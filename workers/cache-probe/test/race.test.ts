import { SELF } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import { drawDelayMs } from "../src/race";

const url = (path: string) => `https://probe.test${path}`;
const post = (path: string) => SELF.fetch(url(path), { method: "POST" });

interface RaceResponse {
  claimed: boolean;
  isolate: string;
  colo: string | null;
  key: string;
  scope: string;
  seq: string | null;
  delayMs: number;
}

describe("drawDelayMs", () => {
  it("draws over [0, J) and collapses to no delay at all when J is zero", () => {
    expect(drawDelayMs(1_000, () => 0)).toBe(0);
    expect(drawDelayMs(1_000, () => 0.5)).toBe(500);
    expect(drawDelayMs(1_000, () => 0.999)).toBeLessThan(1_000);
    expect(
      drawDelayMs(0, () => {
        throw new Error("drew for an un-jittered racer");
      }),
    ).toBe(0);
  });
});

describe("/race", () => {
  it("requires a key", async () => {
    expect((await post("/race")).status).toBe(400);
  });

  it("rejects an unknown scope", async () => {
    expect((await post("/race?key=a&scope=elsewhere")).status).toBe(400);
  });

  it("rejects a non-positive ttl", async () => {
    expect((await post("/race?key=a&ttl=0")).status).toBe(400);
  });

  it("rejects GET, so nothing treats a claim as retriable", async () => {
    expect((await SELF.fetch(url("/race?key=a"))).status).toBe(405);
  });

  it("claims a cold key and reports the racer's identity back", async () => {
    const response = await post("/race?key=cold-1&seq=0");
    const body = await response.json<RaceResponse>();

    expect(body.claimed).toBe(true);
    expect(body.key).toBe("cold-1");
    expect(body.scope).toBe("offzone");
    expect(body.seq).toBe("0");
    expect(body.isolate).toMatch(/^[0-9a-f]{8}$/);
  });

  it("refuses the second claim on a key already claimed", async () => {
    await post("/race?key=warm-1&seq=0");
    const second = await post("/race?key=warm-1&seq=1");

    expect((await second.json<RaceResponse>()).claimed).toBe(false);
  });

  it("keys claims per key and per scope", async () => {
    await post("/race?key=scoped&seq=0&scope=offzone");
    const other = await post("/race?key=scoped&seq=1&scope=onzone");
    const elsewhere = await post("/race?key=scoped-other&seq=2&scope=offzone");

    expect((await other.json<RaceResponse>()).claimed).toBe(true);
    expect((await elsewhere.json<RaceResponse>()).claimed).toBe(true);
  });

  it("rejects a negative jitter rather than reading it as none", async () => {
    expect((await post("/race?key=a&jitter=-5")).status).toBe(400);
    expect((await post("/race?key=a&jitter=abc")).status).toBe(400);
  });

  it("draws no delay at all when no jitter was asked for", async () => {
    const body = await (await post("/race?key=nojitter&seq=0")).json<RaceResponse>();
    expect(body.delayMs).toBe(0);

    const explicit = await (await post("/race?key=nojitter2&seq=0&jitter=0")).json<RaceResponse>();
    expect(explicit.delayMs).toBe(0);
  });

  it("draws and reports a delay inside the window it was given", async () => {
    const body = await (await post("/race?key=jittered&seq=0&jitter=40")).json<RaceResponse>();

    expect(body.delayMs).toBeGreaterThan(0);
    expect(body.delayMs).toBeLessThan(40);
  });

  it("spends the delay it reports, rather than only reporting it", async () => {
    let reported = 0;
    const started = Date.now();
    for (let i = 0; i < 5; i += 1) {
      const body = await (
        await post(`/race?key=spent-${i}&seq=0&jitter=100`)
      ).json<RaceResponse>();
      reported += body.delayMs;
    }

    expect(Date.now() - started).toBeGreaterThanOrEqual(Math.floor(reported));
  });

  it("marks every racing response no-store, so the zone cannot serve one body twice", async () => {
    const response = await post("/race?key=no-store&seq=0");
    expect(response.headers.get("cache-control")).toBe("no-store");
  });
});

interface ControlResponse {
  scope: string;
  mode: string;
  verified?: boolean;
  hit?: boolean;
  writer?: string | null;
  isolate: string;
}

const control = (query: string) => SELF.fetch(url(`/control?${query}`));

describe("/control", () => {
  it("requires a run and a scope", async () => {
    expect((await control("scope=onzone")).status).toBe(400);
    expect((await control("run=a")).status).toBe(400);
  });

  it("rejects an unknown mode", async () => {
    expect((await control("run=a&scope=onzone&mode=delete")).status).toBe(400);
  });

  it("verifies a write from the isolate that made it", async () => {
    const written = await (
      await control("run=ctl-write&scope=onzone&mode=write")
    ).json<ControlResponse>();

    expect(written.verified).toBe(true);
    expect(written.mode).toBe("write");
    expect(written.scope).toBe("onzone");
  });

  it("reads back the writing isolate", async () => {
    const written = await (
      await control("run=ctl-read&scope=offzone&mode=write")
    ).json<ControlResponse>();
    const read = await (
      await control("run=ctl-read&scope=offzone&mode=read")
    ).json<ControlResponse>();

    expect(read.hit).toBe(true);
    expect(read.writer).toBe(written.isolate);
  });

  it("keeps the two scopes' keys apart, so one arm cannot answer for the other", async () => {
    await control("run=ctl-scope&scope=onzone&mode=write");
    const read = await (
      await control("run=ctl-scope&scope=offzone&mode=read")
    ).json<ControlResponse>();

    expect(read.hit).toBe(false);
  });

  it("reports a miss for a run nothing wrote", async () => {
    const read = await (
      await control("run=ctl-never&scope=onzone&mode=read")
    ).json<ControlResponse>();

    expect(read).toMatchObject({ hit: false, writer: null });
  });

  it("marks control responses no-store too", async () => {
    const response = await control("run=ctl-headers&scope=onzone&mode=read");
    expect(response.headers.get("cache-control")).toBe("no-store");
  });
});

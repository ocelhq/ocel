import { describe, expect, it } from "vitest";

import { isServiceThrottle, retryTransientOrigin } from "../src/retry.mjs";

const noDelay = { sleep: async () => {}, random: () => 0 };

function throttled() {
  return new Response(null, { status: 429, headers: { "x-amzn-errortype": "TooManyRequestsException" } });
}

describe("retryTransientOrigin", () => {
  it("returns a successful first attempt without retrying", async () => {
    let calls = 0;
    const res = await retryTransientOrigin(async () => {
      calls++;
      return new Response("ok", { status: 200 });
    }, noDelay);

    expect(res.status).toBe(200);
    expect(calls).toBe(1);
  });

  it("retries a service throttle and serves the response that follows", async () => {
    let calls = 0;
    const res = await retryTransientOrigin(async () => {
      calls++;
      if (calls === 1) return throttled();
      return new Response("served", { status: 200 });
    }, noDelay);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("served");
    expect(calls).toBe(2);
  });

  it("throws once the retry budget is exhausted on repeated throttles", async () => {
    let calls = 0;
    await expect(
      retryTransientOrigin(async () => {
        calls++;
        return throttled();
      }, noDelay),
    ).rejects.toThrow(/429/);
    expect(calls).toBe(3);
  });

  it("retries a thrown connection failure and serves the response that follows", async () => {
    let calls = 0;
    const res = await retryTransientOrigin(async () => {
      calls++;
      if (calls === 1) throw new Error("connection reset");
      return new Response("served", { status: 200 });
    }, noDelay);

    expect(res.status).toBe(200);
    expect(calls).toBe(2);
  });

  it("throws the last error once the budget is exhausted on repeated failures", async () => {
    let calls = 0;
    await expect(
      retryTransientOrigin(async () => {
        calls++;
        throw new Error(`boom ${calls}`);
      }, noDelay),
    ).rejects.toThrow("boom 3");
    expect(calls).toBe(3);
  });

  it("does not retry a real 4xx/5xx the origin produced on purpose", async () => {
    let calls = 0;
    const res = await retryTransientOrigin(async () => {
      calls++;
      return new Response("app error", { status: 500 });
    }, noDelay);

    expect(res.status).toBe(500);
    expect(await res.text()).toBe("app error");
    expect(calls).toBe(1);
  });

  it("does not retry an app-authored 429 (no x-amzn-errortype), and returns it verbatim", async () => {
    let calls = 0;
    const res = await retryTransientOrigin(async () => {
      calls++;
      return new Response("rate limited", {
        status: 429,
        headers: { "retry-after": "60" },
      });
    }, noDelay);

    expect(res.status).toBe(429);
    expect(res.headers.get("retry-after")).toBe("60");
    expect(await res.text()).toBe("rate limited");
    expect(calls).toBe(1);
  });

  it("backs off between attempts using the injected sleep and random", async () => {
    const delays: number[] = [];
    let calls = 0;
    await retryTransientOrigin(
      async () => {
        calls++;
        return calls === 1 ? throttled() : new Response("ok", { status: 200 });
      },
      {
        random: () => 0.5,
        sleep: async (ms) => {
          delays.push(ms);
        },
      },
    );

    expect(delays).toEqual([75]); // 50 * 2^0 * (1 + 0.5)
  });
});

describe("isServiceThrottle", () => {
  it("is true for a 429 carrying x-amzn-errortype", () => {
    expect(isServiceThrottle(throttled())).toBe(true);
  });

  it("is false for a 429 the app authored", () => {
    expect(isServiceThrottle(new Response(null, { status: 429 }))).toBe(false);
  });

  it("is false for a non-429 that happens to carry x-amzn-errortype", () => {
    expect(
      isServiceThrottle(
        new Response(null, { status: 500, headers: { "x-amzn-errortype": "InternalError" } }),
      ),
    ).toBe(false);
  });
});

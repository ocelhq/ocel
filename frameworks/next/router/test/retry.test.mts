import { describe, expect, it } from "vitest";

import { retryTransientOrigin } from "../src/retry.mjs";

const noDelay = { sleep: async () => {}, random: () => 0 };

const THROTTLE_HEADER = "x-host-throttle";

function throttled() {
  return new Response(null, { status: 429, headers: { [THROTTLE_HEADER]: "1" } });
}

const isHostThrottle = (response: Response) =>
  response.status === 429 && response.headers.has(THROTTLE_HEADER);

describe("retryTransientOrigin", () => {
  it("returns a successful first attempt without retrying", async () => {
    let calls = 0;
    const res = await retryTransientOrigin(
      async () => {
        calls++;
        return new Response("ok", { status: 200 });
      },
      isHostThrottle,
      noDelay,
    );

    expect(res.status).toBe(200);
    expect(calls).toBe(1);
  });

  it("retries a service throttle and serves the response that follows", async () => {
    let calls = 0;
    const res = await retryTransientOrigin(
      async () => {
        calls++;
        if (calls === 1) return throttled();
        return new Response("served", { status: 200 });
      },
      isHostThrottle,
      noDelay,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("served");
    expect(calls).toBe(2);
  });

  it("throws once the retry budget is exhausted on repeated throttles", async () => {
    let calls = 0;
    await expect(
      retryTransientOrigin(
        async () => {
          calls++;
          return throttled();
        },
        isHostThrottle,
        noDelay,
      ),
    ).rejects.toThrow(/throttled/);
    expect(calls).toBe(3);
  });

  it("retries a thrown connection failure and serves the response that follows", async () => {
    let calls = 0;
    const res = await retryTransientOrigin(
      async () => {
        calls++;
        if (calls === 1) throw new Error("connection reset");
        return new Response("served", { status: 200 });
      },
      isHostThrottle,
      noDelay,
    );

    expect(res.status).toBe(200);
    expect(calls).toBe(2);
  });

  it("throws the last error once the budget is exhausted on repeated failures", async () => {
    let calls = 0;
    await expect(
      retryTransientOrigin(
        async () => {
          calls++;
          throw new Error(`boom ${calls}`);
        },
        isHostThrottle,
        noDelay,
      ),
    ).rejects.toThrow("boom 3");
    expect(calls).toBe(3);
  });

  it("does not retry a real 4xx/5xx the origin produced on purpose", async () => {
    let calls = 0;
    const res = await retryTransientOrigin(
      async () => {
        calls++;
        return new Response("app error", { status: 500 });
      },
      isHostThrottle,
      noDelay,
    );

    expect(res.status).toBe(500);
    expect(await res.text()).toBe("app error");
    expect(calls).toBe(1);
  });

  it("does not retry a 429 the host's throttle check does not name, and returns it verbatim", async () => {
    let calls = 0;
    const res = await retryTransientOrigin(
      async () => {
        calls++;
        return new Response("rate limited", {
          status: 429,
          headers: { "retry-after": "60" },
        });
      },
      isHostThrottle,
      noDelay,
    );

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
      isHostThrottle,
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

describe("a host's throttle check", () => {
  it("may read the response asynchronously without consuming the body served", async () => {
    let calls = 0;
    const res = await retryTransientOrigin(
      async () => {
        calls++;
        return new Response(calls === 1 ? "busy" : "app said no", { status: 429 });
      },
      async (response) => (await response.clone().text()) === "busy",
      noDelay,
    );

    expect(calls).toBe(2);
    expect(await res.text()).toBe("app said no");
  });

  it("is absent where a host names none, so no response is retried", async () => {
    let calls = 0;
    const res = await retryTransientOrigin(
      async () => {
        calls++;
        return throttled();
      },
      undefined,
      noDelay,
    );

    expect(res.status).toBe(429);
    expect(calls).toBe(1);
  });
});

import assert from "node:assert/strict";
import { describe, it } from "vitest";
import { awaitServing } from "./serving";

function clock(timeoutMs: number) {
  let at = 0;
  return {
    timeoutMs,
    intervalMs: 5_000,
    now: () => at,
    sleep: async (ms: number) => {
      at += ms;
    },
  };
}

function answering(answers: Array<number | string>): typeof fetch {
  let served = 0;
  return (async (input: unknown) => {
    const answer = answers[served] ?? answers[answers.length - 1];
    served += 1;
    if (typeof answer === "string") {
      throw new Error(answer);
    }
    assert.equal(String(input), "https://app.example.com/health");
    return new Response(null, { status: answer });
  }) as unknown as typeof fetch;
}

const URLS = new Map([["web", "https://app.example.com"]]);

describe("awaitServing", () => {
  it("stops at the attempt the edge completes a handshake on", async () => {
    const served = await awaitServing(
      answering(["alert 40", "alert 40", 200]),
      URLS,
      clock(900_000),
    );
    assert.deepEqual(served, {
      web: { host: "app.example.com", waitedMs: 10_000, attempts: 3, status: 200 },
    });
  });

  it("keeps waiting through a 403 and takes the later 200 as the readiness signal", async () => {
    const served = await awaitServing(answering([403, 403, 200]), URLS, clock(900_000));
    assert.deepEqual(served, {
      web: { host: "app.example.com", waitedMs: 10_000, attempts: 3, status: 200 },
    });
  });

  it("gives up at the deadline naming the last status when only 403s came back", async () => {
    await assert.rejects(awaitServing(answering([403]), URLS, clock(10_000)), (error: Error) => {
      assert.match(error.message, /app\.example\.com/);
      assert.match(error.message, /3 attempts/);
      assert.match(error.message, /last status 403/);
      return true;
    });
  });

  it("gives up at the deadline naming the host and the last refusal", async () => {
    await assert.rejects(
      awaitServing(answering(["alert 40"]), URLS, clock(10_000)),
      (error: Error) => {
        assert.match(error.message, /app\.example\.com/);
        assert.match(error.message, /alert 40/);
        return true;
      },
    );
  });

  it("waits for every app in the cell", async () => {
    const served = await awaitServing(
      (async () => new Response(null, { status: 200 })) as unknown as typeof fetch,
      new Map([
        ["web", "https://web.example.com"],
        ["api", "https://api.example.com"],
      ]),
      clock(900_000),
    );
    assert.deepEqual(Object.keys(served), ["web", "api"]);
    assert.equal(served.api?.host, "api.example.com");
  });
});

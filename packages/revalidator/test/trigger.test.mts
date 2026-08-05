import { expect, it, vi } from "vitest";

import { triggerTimeoutMs } from "../src/limits.mjs";
import { trigger, type TriggerDeps } from "../src/trigger.mjs";
import { credentials, host, resolved } from "./fixture.mjs";

function responding(response: Response): { deps: TriggerDeps; requests: Request[] } {
  const requests: Request[] = [];
  const deps: TriggerDeps = {
    credentials,
    fetch: (async (input, init) => {
      requests.push(new Request(input as string | Request, init));
      return response;
    }) as typeof fetch,
  };
  return { deps, requests };
}

function ok(headers: Record<string, string> = {}): Response {
  return new Response(null, { status: 200, headers });
}

it("sends HEAD to the resolved target carrying the message's headers and no others", async () => {
  const { deps, requests } = responding(ok({ "x-nextjs-cache": "REVALIDATED" }));
  const { target, message } = await resolved();

  await trigger(deps, target, message);

  expect(requests).toHaveLength(1);
  const sent = requests[0]!;
  expect(sent.method).toBe("HEAD");
  expect(sent.url).toBe(`https://${host}/blog/post`);
  // Everything the message asked for, and nothing of the message's beyond it:
  // what is left over is the signature and whatever the runtime adds.
  const carried = [...sent.headers.keys()].filter((name) => !name.startsWith("x-amz-") && name !== "authorization");
  expect(carried.sort()).toEqual(["x-forwarded-host", "x-prerender-revalidate"]);
  expect(sent.headers.get("x-prerender-revalidate")).toBe("s3cr3t-preview-mode-id");
  expect(sent.headers.get("x-forwarded-host")).toBe("example.com");
});

it("signs as the function's own role, against the resolved region, over the headers it sends", async () => {
  const { deps, requests } = responding(ok({ "x-nextjs-cache": "REVALIDATED" }));
  const { target, message } = await resolved();

  await trigger(deps, target, message);

  const authorization = requests[0]!.headers.get("authorization") ?? "";
  expect(authorization).toContain("Credential=AKIAEXAMPLE/");
  expect(authorization).toContain("/us-east-1/lambda/aws4_request");
  // Nothing sits inside the TLS session to rewrite a header, so the signature
  // covers the message's headers too rather than `host` alone.
  expect(authorization).toContain("x-forwarded-host");
  expect(authorization).toContain("x-prerender-revalidate");
  expect(requests[0]!.headers.get("x-amz-security-token")).toBe("session");
});

it("succeeds when the declared expectation matches", async () => {
  const { deps } = responding(ok({ "x-nextjs-cache": "REVALIDATED" }));
  const { target, message } = await resolved();

  await expect(trigger(deps, target, message)).resolves.toEqual({ event: "RevalidateOk" });
});

it("succeeds when no expectation is declared", async () => {
  const { deps } = responding(ok());
  const { target, message } = await resolved({ expect: null });

  await expect(trigger(deps, target, message)).resolves.toEqual({ event: "RevalidateOk" });
});

it("reports an expect miss when the declared header disagrees", async () => {
  const { deps } = responding(ok({ "x-nextjs-cache": "STALE" }));
  const { target, message } = await resolved();

  await expect(trigger(deps, target, message)).resolves.toEqual({
    event: "RevalidateExpectMiss",
    expected: "REVALIDATED",
    got: "STALE",
  });
});

it("reports an expect miss when the declared header is absent", async () => {
  const { deps } = responding(ok());
  const { target, message } = await resolved();

  await expect(trigger(deps, target, message)).resolves.toEqual({
    event: "RevalidateExpectMiss",
    expected: "REVALIDATED",
    got: null,
  });
});

it("fails on a non-ok response, carrying the status", async () => {
  const { deps } = responding(new Response(null, { status: 429 }));
  const { target, message } = await resolved();

  await expect(trigger(deps, target, message)).resolves.toEqual({
    event: "RevalidateFailed",
    reason: "status-not-ok",
    status: 429,
  });
});

it("fails when the fetch throws", async () => {
  const { target, message } = await resolved();
  const deps: TriggerDeps = {
    credentials,
    fetch: (async () => {
      throw new Error("connect ECONNREFUSED");
    }) as typeof fetch,
  };

  await expect(trigger(deps, target, message)).resolves.toEqual({
    event: "RevalidateFailed",
    reason: "fetch-failed",
  });
});

it("fails as a timeout when the origin outlasts the budget", async () => {
  const { target, message } = await resolved();
  const deps: TriggerDeps = {
    credentials,
    timeoutMs: 5,
    fetch: ((_input: string, init: RequestInit) =>
      new Promise((_resolve, reject) => {
        init.signal?.addEventListener("abort", () => reject(new Error("aborted")));
      })) as unknown as typeof fetch,
  };

  await expect(trigger(deps, target, message)).resolves.toEqual({
    event: "RevalidateFailed",
    reason: "timeout",
  });
});

// The default budget is the one production runs on — nothing in index.mts
// passes timeoutMs — and it is the single number the function timeout and the
// queue's visibility timeout are sized from. Asserting it means catching both
// the number the signal is built from and that the signal built from it is the
// one the request actually waits on.
it("triggers on the documented budget when no caller overrides it", async () => {
  const { target, message } = await resolved();
  const timeout = vi.spyOn(AbortSignal, "timeout");
  const signals: (AbortSignal | null | undefined)[] = [];
  const deps: TriggerDeps = {
    credentials,
    fetch: (async (_input: string, init: RequestInit) => {
      signals.push(init.signal);
      return ok({ "x-nextjs-cache": "REVALIDATED" });
    }) as typeof fetch,
  };

  await trigger(deps, target, message);

  expect(timeout).toHaveBeenCalledWith(triggerTimeoutMs);
  expect(signals[0]).toBe(timeout.mock.results[0]!.value);
  timeout.mockRestore();
});

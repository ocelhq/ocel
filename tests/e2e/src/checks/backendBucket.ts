import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { until } from "../nextApp";
import type { Check, CheckContext } from "./context";

const IMMUTABLE_PREFIX = "/_next/static/";
const STATIC_FILE = /["'(](\/_next\/static\/[^"'()\s\\?#]+\.(?:js|css))/;

export type BucketTiming = {
  propagationMs: number;
  pollMs: number;
  loggingMs: number;
  settleMs: number;
  strictAttempts: number;
};

const TIMING: BucketTiming = {
  propagationMs: 10 * 60_000,
  pollMs: 5_000,
  loggingMs: 5 * 60_000,
  settleMs: 30_000,
  strictAttempts: 3,
};

type Answer = {
  url: string;
  status: number;
  body: Uint8Array;
  cacheControl: string;
  logged: number;
};

type Verdict = (answer: Answer) => string | undefined;

export function immutableFileOf(html: string): string | undefined {
  return STATIC_FILE.exec(html)?.[1];
}

async function immutableFileOfApp(ctx: CheckContext): Promise<string> {
  const res = await ctx.fetch(`${ctx.baseUrl}/`);
  assert.equal(res.status, 200, `/ answered ${res.status}`);
  const file = immutableFileOf(await res.text());
  assert.ok(
    file,
    `/ links no file under ${IMMUTABLE_PREFIX}, so there is no immutable file to ask for`,
  );
  return file;
}

async function ask(
  ctx: CheckContext,
  timing: BucketTiming,
  askFor: (stamp: string) => string,
  parameter: string,
): Promise<Answer> {
  const stamp = randomUUID();
  const url = askFor(stamp);
  const asked = await ctx.fetch(url);
  const body = new Uint8Array(await asked.arrayBuffer());
  const marker = randomUUID();
  const reached = `${ctx.baseUrl}/?e2e-dynamic=${marker}`;
  const control = await ctx.fetch(reached);
  assert.equal(control.status, 200, `${reached} answered ${control.status}`);
  await control.arrayBuffer();
  await until(
    timing.loggingMs,
    `Cloud Run logged no request for ${reached}, so its request log cannot yet vouch for the requests it did not log`,
    async () => ((await ctx.countOriginRequests(`e2e-dynamic=${marker}`)) > 0 ? true : undefined),
    timing.pollMs,
  );
  await new Promise((resolve) => setTimeout(resolve, timing.settleMs));
  return {
    url,
    status: asked.status,
    body,
    cacheControl: asked.headers.get("cache-control") ?? "",
    logged: await ctx.countOriginRequests(`${parameter}=${stamp}`),
  };
}

function unlogged(answer: Answer): string | undefined {
  return answer.logged === 0
    ? undefined
    : `Cloud Run logged ${answer.logged} request(s) for ${answer.url}, so the load balancer sent it to the service rather than the backend bucket`;
}

async function servedFromBucketThroughout(
  ctx: CheckContext,
  timing: BucketTiming,
  askFor: (stamp: string) => string,
  parameter: string,
  verdict: Verdict,
): Promise<void> {
  const deadline = Date.now() + timing.propagationMs;
  let established: Answer | undefined;
  for (;;) {
    const answer = await ask(ctx, timing, askFor, parameter);
    const why = verdict(answer);
    if (why === undefined) {
      established = answer;
      break;
    }
    assert.ok(
      Date.now() < deadline,
      `no request was served from the backend bucket within ${timing.propagationMs}ms: ${why}`,
    );
    await new Promise((resolve) => setTimeout(resolve, timing.pollMs));
  }
  for (let attempt = 0; attempt < timing.strictAttempts; attempt += 1) {
    const answer = await ask(ctx, timing, askFor, parameter);
    const why = verdict(answer);
    assert.ok(
      why === undefined,
      `after ${established.url} was served from the backend bucket, ${why}`,
    );
  }
}

function sameBytes(a: Uint8Array, b: Uint8Array): boolean {
  return a.length === b.length && a.every((byte, at) => byte === b[at]);
}

export function backendBucketChecksWith(timing: BucketTiming): Check[] {
  return [
    {
      title:
        "an immutable file is the current release's upload, served from the backend bucket with no Cloud Run request logged",
      run: async (ctx) => {
        const file = await immutableFileOfApp(ctx);
        const uploaded = await ctx.readReleaseFile(file);
        await servedFromBucketThroughout(
          ctx,
          timing,
          (stamp) => `${ctx.baseUrl}${file}?e2e-static=${stamp}`,
          "e2e-static",
          (answer) => {
            if (answer.status !== 200) {
              return `${answer.url} answered ${answer.status}, want 200`;
            }
            if (!sameBytes(answer.body, uploaded)) {
              return `${answer.url} answered ${answer.body.length} bytes that are not the bytes the current release uploaded (${uploaded.length})`;
            }
            if (!/immutable/.test(answer.cacheControl)) {
              return `${answer.url} was served with cache control "${answer.cacheControl}", not the immutable one it was uploaded with`;
            }
            return unlogged(answer);
          },
        );
      },
    },
    {
      title: "a file missing under an immutable prefix is a 404 with no Cloud Run request logged",
      run: async (ctx) => {
        await servedFromBucketThroughout(
          ctx,
          timing,
          (stamp) =>
            `${ctx.baseUrl}${IMMUTABLE_PREFIX}chunks/missing-${stamp}.js?e2e-missing=${stamp}`,
          "e2e-missing",
          (answer) =>
            answer.status === 404
              ? unlogged(answer)
              : `${answer.url} answered ${answer.status}, want 404`,
        );
      },
    },
  ];
}

export const backendBucketChecks: Check[] = backendBucketChecksWith(TIMING);

import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { until } from "../nextApp";
import type { Check, CheckContext } from "./context";

const IMMUTABLE_PREFIX = "/_next/static/";
const STATIC_FILE = /["'(](\/_next\/static\/[^"'()\s\\?#]+\.(?:js|css))/;
const ATTEMPTS = 4;
const LOGGING_MS = 5 * 60_000;
const POLL_MS = 5_000;

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

async function loggedAfterAsking(
  ctx: CheckContext,
  url: string,
  fragment: string,
  expected: number,
): Promise<number> {
  const asked = await ctx.fetch(url);
  assert.equal(asked.status, expected, `${url} answered ${asked.status}, want ${expected}`);
  await asked.arrayBuffer();
  const marker = randomUUID();
  const reached = `${ctx.baseUrl}/?e2e-dynamic=${marker}`;
  const control = await ctx.fetch(reached);
  assert.equal(control.status, 200, `${reached} answered ${control.status}`);
  await control.arrayBuffer();
  await until(
    LOGGING_MS,
    `Cloud Run logged no request for ${reached}, so its request log cannot yet vouch for the requests it did not log`,
    async () => ((await ctx.countOriginRequests(`e2e-dynamic=${marker}`)) > 0 ? true : undefined),
    POLL_MS,
  );
  return ctx.countOriginRequests(fragment);
}

async function neverLogged(
  ctx: CheckContext,
  askFor: (stamp: string) => string,
  parameter: string,
  expected: number,
): Promise<void> {
  const seen: string[] = [];
  for (let attempt = 0; attempt < ATTEMPTS; attempt += 1) {
    const stamp = randomUUID();
    const url = askFor(stamp);
    const logged = await loggedAfterAsking(ctx, url, `${parameter}=${stamp}`, expected);
    if (logged === 0) {
      return;
    }
    seen.push(`${url} (${logged})`);
  }
  assert.fail(
    `Cloud Run logged a request for every url asked, so the load balancer sent them to the service rather than the backend bucket: ${seen.join(", ")}`,
  );
}

export const backendBucketChecks: Check[] = [
  {
    title: "an immutable file is served from the backend bucket with no Cloud Run request logged",
    run: async (ctx) => {
      const file = await immutableFileOfApp(ctx);
      await neverLogged(
        ctx,
        (stamp) => `${ctx.baseUrl}${file}?e2e-static=${stamp}`,
        "e2e-static",
        200,
      );
      const served = await ctx.fetch(`${ctx.baseUrl}${file}`);
      assert.equal(served.status, 200);
      await served.arrayBuffer();
      assert.match(
        served.headers.get("cache-control") ?? "",
        /immutable/,
        `${file} was served without the immutable cache control it was uploaded with`,
      );
    },
  },
  {
    title: "a file missing under an immutable prefix is a 404 with no Cloud Run request logged",
    run: async (ctx) => {
      await neverLogged(
        ctx,
        (stamp) =>
          `${ctx.baseUrl}${IMMUTABLE_PREFIX}chunks/missing-${stamp}.js?e2e-missing=${stamp}`,
        "e2e-missing",
        404,
      );
    },
  },
];

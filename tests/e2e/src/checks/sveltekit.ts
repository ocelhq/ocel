import assert from "node:assert/strict";
import type { Check, CheckContext } from "./context";

const IMMUTABLE_CHUNK = /\/_app\/immutable\/[^"'\s]+\.js/;

async function page(ctx: CheckContext, path: string): Promise<{ res: Response; html: string }> {
  const res = await ctx.fetch(`${ctx.baseUrl}${path}`, { headers: { accept: "text/html" } });
  return { res, html: await res.text() };
}

function postForm(ctx: CheckContext, origin: string): Promise<Response> {
  return ctx.fetch(`${ctx.baseUrl}/form`, {
    method: "POST",
    headers: {
      origin,
      accept: "application/json",
      "content-type": "application/x-www-form-urlencoded",
    },
    body: "message=from-the-journey",
  });
}

export const publicOriginFormActionCheck: Check = {
  title: "a form action posted from the app's public origin runs",
  run: async (ctx) => {
    const res = await postForm(ctx, new URL(ctx.baseUrl).origin);
    const text = await res.text();
    assert.equal(res.status, 200, text);
    const result = JSON.parse(text) as { type: string; data: string };
    assert.equal(result.type, "success", text);
    assert.match(result.data, /from-the-journey/);
  },
};

export const svelteKitChecks: Check[] = [
  {
    title: "GET / is rendered on the server",
    run: async (ctx) => {
      const { res, html } = await page(ctx, "/");
      assert.equal(res.status, 200);
      assert.match(res.headers.get("content-type") ?? "", /^text\/html/);
      assert.match(html, /data-rendered-at="\d+"/);
    },
  },
  {
    title: "GET /prerendered serves the page prerendered at build, revalidated",
    assertsDeployment: true,
    run: async (ctx) => {
      const { res, html } = await page(ctx, "/prerendered");
      assert.equal(res.status, 200);
      assert.match(html, /prerendered at build/);
      assert.doesNotMatch(res.headers.get("cache-control") ?? "", /immutable/);
    },
  },
  {
    title: "a content-hashed chunk the page loads is cached for a year",
    assertsDeployment: true,
    run: async (ctx) => {
      const { html } = await page(ctx, "/");
      const chunk = IMMUTABLE_CHUNK.exec(html)?.[0];
      assert.ok(chunk, "the page links no /_app/immutable/ chunk");
      const res = await ctx.fetch(`${ctx.baseUrl}${chunk}`);
      assert.equal(res.status, 200);
      assert.match(res.headers.get("content-type") ?? "", /javascript/);
      assert.match(res.headers.get("cache-control") ?? "", /max-age=31536000.*immutable/);
    },
  },
  {
    title: "GET /_app/version.json is revalidated, so a client learns of a new deployment",
    assertsDeployment: true,
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/_app/version.json`);
      assert.equal(res.status, 200);
      assert.doesNotMatch(res.headers.get("cache-control") ?? "", /immutable/);
    },
  },
  {
    title: "a chunk missing from /_app/immutable/ is a 404, not a rendered page",
    assertsDeployment: true,
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/_app/immutable/chunks/missing.js`);
      assert.equal(res.status, 404);
      assert.doesNotMatch(await res.text(), /<html/i);
    },
  },
  publicOriginFormActionCheck,
  {
    title: "a form action posted from another origin is refused",
    run: async (ctx) => {
      const res = await postForm(ctx, "https://elsewhere.invalid");
      assert.equal(res.status, 403);
    },
  },
];

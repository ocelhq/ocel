import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import type { ContractContext, ContractRow } from "./contract";
import { chunksOf, firstChunkWith, form, marker, stamp } from "./html";

const AFTER_TIMEOUT_MS = 10_000;
const METHODS_WITH_BODIES = ["POST", "PUT", "PATCH", "DELETE"] as const;

type StateRow = {
  key: string;
  count: number;
  firstSeen: string;
  lastSeen: string;
  value: string | null;
};

async function html(ctx: ContractContext, path: string): Promise<string> {
  const res = await ctx.fetch(`${ctx.baseUrl}${path}`);
  assert.equal(res.status, 200, `${path} answered ${res.status}`);
  return res.text();
}

async function text(ctx: ContractContext, path: string, init?: RequestInit) {
  const res = await ctx.fetch(`${ctx.baseUrl}${path}`, init);
  return { res, body: await res.text() };
}

async function state(ctx: ContractContext): Promise<Map<string, StateRow>> {
  const res = await ctx.fetch(`${ctx.baseUrl}/api/next/state`);
  assert.equal(res.status, 200, "the state readback answered " + res.status);
  const read = (await res.json()) as { rows: StateRow[] };
  return new Map(read.rows.map((row) => [row.key, row]));
}

async function stateRow(ctx: ContractContext, key: string, timeoutMs: number): Promise<StateRow> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const row = (await state(ctx)).get(key);
    if (row) {
      return row;
    }
    assert.ok(Date.now() < deadline, `${key} never reached the state readback`);
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
}

function locationPath(res: Response): string {
  const location = res.headers.get("location");
  assert.ok(location, `a ${res.status} carried no Location`);
  const url = new URL(location, "http://redirect.invalid");
  return `${url.pathname}${url.search}`;
}

async function submit(
  ctx: ContractContext,
  path: string,
  values: Record<string, string>,
): Promise<Response> {
  const posted = form(await html(ctx, path));
  assert.equal(posted.method, "post");
  const body = new FormData();
  for (const [name, value] of Object.entries({ ...posted.fields, ...values })) {
    body.append(name, value);
  }
  const target = new URL(posted.action || path, `${ctx.baseUrl}${path}`);
  return ctx.fetch(target.toString(), { method: "POST", body, redirect: "manual" });
}

export const nextRoutingRows: ContractRow[] = [
  {
    title: "the next handler answers every method with the body it was sent",
    suite: "next-routing",
    run: async (ctx) => {
      const read = await text(ctx, "/api/next/methods");
      assert.equal(read.res.status, 200);
      assert.match(read.res.headers.get("content-type") ?? "", /^text\/plain/);
      assert.equal(read.body, "GET:");

      const head = await ctx.fetch(`${ctx.baseUrl}/api/next/methods`, { method: "HEAD" });
      assert.equal(head.status, 200);
      assert.equal(head.headers.get("x-ocel-method"), "HEAD");

      const options = await ctx.fetch(`${ctx.baseUrl}/api/next/methods`, { method: "OPTIONS" });
      assert.equal(options.status, 200);
      assert.equal(options.headers.get("x-ocel-method"), "OPTIONS");

      for (const method of METHODS_WITH_BODIES) {
        const sent = `body-for-${method}`;
        const { res, body } = await text(ctx, "/api/next/methods", {
          method,
          headers: { "content-type": "text/plain" },
          body: sent,
        });
        assert.equal(res.status, 200);
        assert.equal(body, `${method}:${sent}`);
      }
    },
  },
  {
    title: "the tenant route answers every method with its param and body",
    suite: "next-routing",
    run: async (ctx) => {
      const read = await text(ctx, "/api/next/tenants/acme");
      assert.equal(read.res.status, 200);
      assert.equal(read.body, "GET:acme:");

      for (const method of METHODS_WITH_BODIES) {
        const sent = `body-for-${method}`;
        const { res, body } = await text(ctx, "/api/next/tenants/acme", {
          method,
          headers: { "content-type": "text/plain" },
          body: sent,
        });
        assert.equal(res.status, 200);
        assert.equal(body, `${method}:acme:${sent}`);
      }
    },
  },
  {
    title: "a dynamic route decodes its param exactly once",
    suite: "next-routing",
    run: async (ctx) => {
      assert.equal(
        marker(await html(ctx, "/routing/dynamic/one%2520two"), "param:slug"),
        "one%20two",
      );
    },
  },
  {
    title: "a catch-all route splits its once-decoded segments",
    suite: "next-routing",
    run: async (ctx) => {
      assert.equal(
        marker(await html(ctx, "/routing/catch-all/alpha/one%2520two"), "param:parts"),
        "alpha|one%20two",
      );
    },
  },
  {
    title: "an optional catch-all route serves with and without segments",
    suite: "next-routing",
    run: async (ctx) => {
      assert.equal(marker(await html(ctx, "/routing/optional"), "param:parts"), "none");
      assert.equal(marker(await html(ctx, "/routing/optional/x/y"), "param:parts"), "x|y");
    },
  },
  {
    title: "an unmatched path renders the not-found page and notFound() answers 404",
    suite: "next-routing",
    run: async (ctx) => {
      const unmatched = await text(ctx, "/nothing-is-mounted-here");
      assert.equal(unmatched.res.status, 404);
      assert.equal(marker(unmatched.body, "page"), "not-found");

      const called = await ctx.fetch(`${ctx.baseUrl}/routing/missing`);
      assert.equal(called.status, 404);
      assert.match(called.headers.get("content-type") ?? "", /^text\/html/);
    },
  },
  {
    title: "a throwing page renders the error page with a 500",
    suite: "next-routing",
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/routing/boom`);
      assert.equal(res.status, 500);
    },
  },
  {
    title: "a bogus static asset is a plain-text 404",
    suite: "next-routing",
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/_next/static/chunks/ocel-bogus.js`);
      assert.equal(res.status, 404);
      assert.match(res.headers.get("content-type") ?? "", /^text\/plain/);
    },
  },
  {
    title: "config redirects answer 307 and 308 to the same landing page",
    suite: "next-routing",
    run: async (ctx) => {
      for (const [path, status] of [
        ["/routing/redirect/temporary", 307],
        ["/routing/redirect/permanent", 308],
      ] as const) {
        const res = await ctx.fetch(`${ctx.baseUrl}${path}`, { redirect: "manual" });
        assert.equal(res.status, status, `${path} answered ${res.status}`);
        assert.equal(locationPath(res), "/routing/landing");
      }
    },
  },
  {
    title: "config redirects fire only when has matches and missing does not",
    suite: "next-routing",
    run: async (ctx) => {
      const matched = await ctx.fetch(`${ctx.baseUrl}/routing/redirect/has?to=landing`, {
        redirect: "manual",
      });
      assert.equal(matched.status, 307);
      assert.equal(locationPath(matched), "/routing/landing?to=landing");
      const unmatched = await ctx.fetch(`${ctx.baseUrl}/routing/redirect/has`, {
        redirect: "manual",
      });
      assert.equal(unmatched.status, 404);

      const absent = await ctx.fetch(`${ctx.baseUrl}/routing/redirect/missing`, {
        redirect: "manual",
      });
      assert.equal(absent.status, 307);
      assert.equal(locationPath(absent), "/routing/landing");
      const present = await ctx.fetch(`${ctx.baseUrl}/routing/redirect/missing`, {
        headers: { "x-ocel-stay": "yes" },
        redirect: "manual",
      });
      assert.equal(present.status, 404);
    },
  },
  {
    title: "a redirect beats a beforeFiles rewrite, which beats the filesystem",
    suite: "next-routing",
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/routing/precedence-redirect`, {
        redirect: "manual",
      });
      assert.equal(res.status, 307);
      assert.equal(locationPath(res), "/routing/landing");

      assert.equal(marker(await html(ctx, "/routing/rewrite/before"), "page"), "target:before");
    },
  },
  {
    title: "an afterFiles rewrite loses to the filesystem and beats a dynamic route",
    suite: "next-routing",
    run: async (ctx) => {
      assert.equal(marker(await html(ctx, "/routing/rewrite/after"), "page"), "filesystem");
      assert.equal(
        marker(await html(ctx, "/routing/dynamic/rewritten"), "page"),
        "target:after",
      );
    },
  },
  {
    title: "a fallback rewrite serves what nothing else matched",
    suite: "next-routing",
    run: async (ctx) => {
      assert.equal(
        marker(await html(ctx, "/routing/fallback/anything/at/all"), "page"),
        "target:fallback",
      );
    },
  },
  {
    title: "a config header rides on every routing page",
    suite: "next-routing",
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/routing/landing`);
      assert.equal(res.status, 200);
      assert.equal(res.headers.get("x-ocel-config"), "routing");
    },
  },
  {
    title: "the proxy rewrites, redirects and blocks",
    suite: "next-routing",
    run: async (ctx) => {
      assert.equal(marker(await html(ctx, "/mw/rewrite"), "page"), "mw:target");

      const redirected = await ctx.fetch(`${ctx.baseUrl}/mw/redirect`, { redirect: "manual" });
      assert.equal(redirected.status, 307);
      assert.equal(locationPath(redirected), "/mw/landing");

      const blocked = await text(ctx, "/mw/block");
      assert.equal(blocked.res.status, 403);
      assert.equal(blocked.body, "blocked by the proxy");
    },
  },
  {
    title: "the proxy injects a request header the page renders",
    suite: "next-routing",
    run: async (ctx) => {
      assert.equal(marker(await html(ctx, "/mw/inject"), "header:injected"), "from-the-proxy");
    },
  },
  {
    title: "the proxy sets a cookie when it falls through",
    suite: "next-routing",
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/mw/plain`);
      assert.equal(res.status, 200);
      const cookie = res.headers.get("set-cookie") ?? "";
      assert.match(cookie, /ocel-proxy=fell-through/);
      assert.equal(marker(await res.text(), "page"), "mw:plain");
    },
  },
  {
    title: "the stream page sends its shell before its deferred body",
    suite: "next-routing",
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/stream`);
      assert.equal(res.status, 200);
      const chunks = await chunksOf(res);
      assert.ok(
        firstChunkWith(chunks, "ocel-shell") < firstChunkWith(chunks, "ocel-deferred"),
        "ocel-deferred did not arrive after ocel-shell",
      );
    },
  },
  {
    title: "a server action runs from a real form post",
    suite: "next-routing",
    run: async (ctx) => {
      const note = randomUUID();
      const res = await submit(ctx, "/actions", { note });
      assert.ok(res.status < 400, `the action post answered ${res.status}`);
      await res.arrayBuffer();
      const row = await stateRow(ctx, `action:${note}`, AFTER_TIMEOUT_MS);
      assert.equal(row.count, 1);
    },
  },
  {
    title: "a streaming action sends its shell before its deferred body",
    suite: "next-routing",
    run: async (ctx) => {
      const note = randomUUID();
      const res = await submit(ctx, "/actions/stream", { note });
      assert.ok(res.status < 400, `the streaming action post answered ${res.status}`);
      const chunks = await chunksOf(res);
      assert.ok(
        firstChunkWith(chunks, "ocel-shell") < firstChunkWith(chunks, "ocel-deferred"),
        "ocel-deferred did not arrive after ocel-shell",
      );
      const row = await stateRow(ctx, `action:${note}`, AFTER_TIMEOUT_MS);
      assert.equal(row.count, 1);
    },
  },
  {
    title: "after() lands in state within ten seconds",
    suite: "next-routing",
    run: async (ctx) => {
      const { res, body } = await text(ctx, "/api/next/after");
      assert.equal(res.status, 200);
      const row = await stateRow(ctx, `after:${body}`, AFTER_TIMEOUT_MS);
      assert.equal(row.count, 1);
    },
  },
  {
    title: "register() is counted with a first-seen before the first request",
    suite: "next-routing",
    run: async (ctx) => {
      const probe = await text(ctx, "/api/next/upstream/register-probe");
      assert.equal(probe.res.status, 200);
      const rows = await state(ctx);
      const registered = rows.get("register");
      const requested = rows.get("upstream:register-probe");
      assert.ok(registered, "register() never reached the state readback");
      assert.ok(requested, "the probe request never reached the state readback");
      assert.ok(registered.count >= 1, `register() was counted ${registered.count} times`);
      assert.ok(
        Date.parse(registered.firstSeen) <= Date.parse(requested.firstSeen),
        `register() was first seen at ${registered.firstSeen}, after ${requested.firstSeen}`,
      );
    },
  },
  {
    title: "the edge and the node runtime stamp themselves apart",
    suite: "next-routing",
    run: async (ctx) => {
      const node = stamp(await html(ctx, "/runtime/node"), "runtime");
      const edge = stamp(await html(ctx, "/runtime/edge"), "runtime");
      assert.equal(node.cached, "nodejs");
      assert.equal(edge.cached, "edge");
      assert.notEqual(node.cached, edge.cached);
      assert.ok(node.live.length > 0 && edge.live.length > 0);
    },
  },
];

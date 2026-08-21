#!/usr/bin/env node

const base = process.argv[2] || process.env.NEXT_TEST_DEPLOY_URL;
if (!base) {
  fail("no deployment url given (argument or $NEXT_TEST_DEPLOY_URL)");
}

const rootBody = await fetchText("/");

await assertRewrite();
await assertRedirect();
await assertBlocked();
await assertCookieStamp();

log("all proxy.ts assertions passed");

async function assertRewrite() {
  const body = await fetchText("/mw/rewrite");
  if (body !== rootBody) {
    fail(`/mw/rewrite served a different body than / — the rewrite did not land:\n${body}`);
  }
  log("rewrite: /mw/rewrite served the root page's body");
}

async function assertRedirect() {
  const response = await fetch(new URL("/mw/redirect", base), { redirect: "manual" });
  const location = response.headers.get("location");
  const target = location ? new URL(location, base).pathname : null;
  if (response.status < 300 || response.status >= 400 || target !== "/") {
    fail(
      `/mw/redirect answered status ${response.status} location ${location} — expected a 3xx redirect to /`,
    );
  }
  log(`redirect: /mw/redirect answered ${response.status} -> ${location}`);
}

async function assertBlocked() {
  const response = await fetch(new URL("/mw/blocked", base));
  const body = await response.text();
  if (response.status !== 403 || !body.includes("blocked by proxy.ts")) {
    fail(`/mw/blocked answered status ${response.status} body ${JSON.stringify(body)} — expected 403 "blocked by proxy.ts"`);
  }
  log("direct response: /mw/blocked answered 403 from proxy.ts, never reaching a route");
}

async function assertCookieStamp() {
  const response = await fetch(new URL("/", base));
  await response.text();
  const cookies = response.headers.getSetCookie?.() ?? [];
  if (!cookies.some((c) => c.startsWith("ocel-proxy-seen=1"))) {
    fail(
      `/ carried no ocel-proxy-seen cookie (Set-Cookie: ${cookies.join(", ") || "(none)"}) — proxy.ts's ` +
        `NextResponse.next() touch on the fall-through path did not reach the client`,
    );
  }
  log("fall-through: / carries the cookie proxy.ts stamps on every unmatched path");
}

async function fetchText(path) {
  const response = await fetch(new URL(path, base), { redirect: "manual" });
  const body = await response.text();
  if (response.status !== 200) {
    fail(`${path} answered status ${response.status}, not 200: ${body}`);
  }
  return body;
}

function log(message) {
  process.stdout.write(`[ocel-e2e] proxy: ${message}\n`);
}

function fail(message) {
  process.stderr.write(`[ocel-e2e] proxy assertion failed: ${message}\n`);
  process.exit(1);
}

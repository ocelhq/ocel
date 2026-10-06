import { generateKeyPairSync, type KeyObject, sign } from "node:crypto";
import { expect, test } from "vitest";
import { newGoogleIdTokenCheck } from "../src/next/google-id-token.mjs";

const url = "https://ocel-shop-production-web-123456789.europe-west1.run.app/_ocel/refresh";
const account = "ocel-production-refresh@acme-prod.iam.gserviceaccount.com";
const start = 1_800_000_000_000;

function newKey(kid: string) {
  const { publicKey, privateKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const jwk = { ...publicKey.export({ format: "jwk" }), kid, alg: "RS256", use: "sig" };
  return { jwk, privateKey };
}

const k1 = newKey("k1");
const k2 = newKey("k2");

function b64(value: unknown): string {
  return Buffer.from(JSON.stringify(value)).toString("base64url");
}

function goodPayload(now = start): Record<string, unknown> {
  return {
    iss: "https://accounts.google.com",
    aud: url,
    email: account,
    email_verified: true,
    iat: now / 1000,
    exp: now / 1000 + 3600,
    sub: "1137",
    azp: "1137",
  };
}

function token(
  payload: Record<string, unknown>,
  options: { key?: KeyObject; kid?: string; header?: Record<string, unknown> } = {},
): string {
  const h = b64(options.header ?? { alg: "RS256", kid: options.kid ?? "k1", typ: "JWT" });
  const p = b64(payload);
  const signature = sign("RSA-SHA256", Buffer.from(`${h}.${p}`), options.key ?? k1.privateKey);
  return `${h}.${p}.${signature.toString("base64url")}`;
}

type Reply = () => Response;

function setup(replies: Reply[] = []) {
  let calls = 0;
  const sleeps: number[] = [];
  const clock = { now: start };
  const fetchStub = (async () => {
    const reply = replies[Math.min(calls, replies.length - 1)];
    calls++;
    await Promise.resolve();
    return reply
      ? reply()
      : Response.json({ keys: [k1.jwk] }, { headers: { "cache-control": "public, max-age=3600" } });
  }) as typeof fetch;
  const check = newGoogleIdTokenCheck({
    audience: url,
    email: account,
    fetch: fetchStub,
    now: () => clock.now,
    sleep: (ms) => {
      sleeps.push(ms);
      return Promise.resolve();
    },
    random: () => 0,
  });
  return { check, clock, sleeps, calls: () => calls };
}

const bearer = (jwt: string) => `Bearer ${jwt}`;

test("a token Google signed for the refresh account and url is accepted", async () => {
  const s = setup();
  expect(await s.check(bearer(token(goodPayload())))).toBe(true);
});

test("a token issued as accounts.google.com without a scheme is accepted", async () => {
  const s = setup();
  const jwt = token({ ...goodPayload(), iss: "accounts.google.com" });
  expect(await s.check(bearer(jwt))).toBe(true);
});

test("a token for another audience is refused", async () => {
  const s = setup();
  expect(await s.check(bearer(token({ ...goodPayload(), aud: `${url}x` })))).toBe(false);
  expect(await s.check(bearer(token({ ...goodPayload(), aud: [url] })))).toBe(false);
});

test("a token for another account is refused", async () => {
  const s = setup();
  const jwt = token({ ...goodPayload(), email: "other@acme-prod.iam.gserviceaccount.com" });
  expect(await s.check(bearer(jwt))).toBe(false);
});

test("a token whose account Google has not verified is refused", async () => {
  const s = setup();
  expect(await s.check(bearer(token({ ...goodPayload(), email_verified: false })))).toBe(false);
  expect(await s.check(bearer(token({ ...goodPayload(), email_verified: "true" })))).toBe(false);
});

test("a token from another issuer is refused", async () => {
  const s = setup();
  const jwt = token({ ...goodPayload(), iss: "https://evil.example.com" });
  expect(await s.check(bearer(jwt))).toBe(false);
});

test("an expired token is refused", async () => {
  const s = setup();
  const jwt = token({ ...goodPayload(), exp: start / 1000 - 1 });
  expect(await s.check(bearer(jwt))).toBe(false);
});

test("a token issued in the future is refused", async () => {
  const s = setup();
  expect(await s.check(bearer(token({ ...goodPayload(), iat: start / 1000 + 120 })))).toBe(false);
  expect(await s.check(bearer(token({ ...goodPayload(), iat: start / 1000 + 30 })))).toBe(true);
});

test("a token whose signature does not match its claims is refused", async () => {
  const s = setup();
  const [h, , sig] = token(goodPayload()).split(".");
  const swapped = `${h}.${b64({ ...goodPayload(), email: "other@x.com" })}.${sig}`;
  expect(await s.check(bearer(swapped))).toBe(false);
});

test("a token that names no signing algorithm Google uses is refused without reading Google's keys", async () => {
  const s = setup();
  const none = `${b64({ alg: "none", kid: "k1" })}.${b64(goodPayload())}.x`;
  const hs = token(goodPayload(), { header: { alg: "HS256", kid: "k1" } });
  const noKid = token(goodPayload(), { header: { alg: "RS256" } });
  for (const jwt of [none, hs, noKid]) expect(await s.check(bearer(jwt))).toBe(false);
  expect(s.calls()).toBe(0);
});

test("a request with no bearer token is refused without reading Google's keys", async () => {
  const s = setup();
  for (const header of [undefined, "Basic x", "Bearer a.b", "Bearer a..c", "Bearer a.b.c d"]) {
    expect(await s.check(header)).toBe(false);
  }
  expect(s.calls()).toBe(0);
});

test("a token signed with a key Google does not publish is refused", async () => {
  const s = setup();
  expect(await s.check(bearer(token(goodPayload(), { key: k2.privateKey, kid: "k2" })))).toBe(
    false,
  );
  expect(s.calls()).toBe(2);
});

test("Google's keys are read once and reused for as long as they may be cached", async () => {
  const s = setup([
    () =>
      Response.json(
        { keys: [k1.jwk] },
        { headers: { "cache-control": "public, max-age=1000, must-revalidate", age: "400" } },
      ),
  ]);
  const jwt = bearer(token(goodPayload()));
  expect(await s.check(jwt)).toBe(true);
  s.clock.now += 599_000;
  expect(await s.check(jwt)).toBe(true);
  expect(s.calls()).toBe(1);
  s.clock.now += 1_000;
  expect(await s.check(jwt)).toBe(true);
  expect(s.calls()).toBe(2);
});

test("Google's keys are cached for an hour when they name no lifetime", async () => {
  const s = setup([() => Response.json({ keys: [k1.jwk] })]);
  const jwt = bearer(token(goodPayload()));
  await s.check(jwt);
  s.clock.now += 3_599_000;
  await s.check(jwt);
  expect(s.calls()).toBe(1);
  s.clock.now += 1_000;
  await s.check(jwt);
  expect(s.calls()).toBe(2);
});

test("a key Google rotated in is fetched once, not on every request", async () => {
  const s = setup();
  const unknown = bearer(token(goodPayload(), { key: k2.privateKey, kid: "k2" }));
  await s.check(unknown);
  expect(s.calls()).toBe(2);
  await s.check(unknown);
  await s.check(unknown);
  expect(s.calls()).toBe(2);
  s.clock.now += 60_000;
  await s.check(unknown);
  expect(s.calls()).toBe(3);
});

test("checks arriving together share one read of Google's keys", async () => {
  const s = setup();
  const jwt = bearer(token(goodPayload()));
  const results = await Promise.all([s.check(jwt), s.check(jwt), s.check(jwt)]);
  expect(results).toEqual([true, true, true]);
  expect(s.calls()).toBe(1);
});

test("Google's keys briefly unreadable are read again before the token is judged", async () => {
  const s = setup([
    () => new Response("down", { status: 503 }),
    () => Response.json({ keys: [k1.jwk] }, { headers: { "cache-control": "max-age=3600" } }),
  ]);
  expect(await s.check(bearer(token(goodPayload())))).toBe(true);
  expect(s.calls()).toBe(2);
  expect(s.sleeps).toEqual([0]);
});

test("Google's keys that fail with a client error are not read again", async () => {
  const s = setup([() => new Response("no", { status: 403 })]);
  await expect(s.check(bearer(token(goodPayload())))).rejects.toThrow(/: 403/);
  expect(s.calls()).toBe(1);
});

test("Google's keys that cannot be parsed fail the check and are not cached", async () => {
  const s = setup([
    () => new Response("not json", { status: 200 }),
    () => Response.json({ keys: [k1.jwk] }, { headers: { "cache-control": "max-age=3600" } }),
  ]);
  const jwt = bearer(token(goodPayload()));
  await expect(s.check(jwt)).rejects.toThrow(/could not read Google's token signing keys/);
  s.clock.now += 10_000;
  expect(await s.check(jwt)).toBe(true);
});

test("Google's keys staying unreadable fail the check without naming the token", async () => {
  const s = setup([() => new Response("down", { status: 503 })]);
  const jwt = token(goodPayload());
  const failure = await s.check(bearer(jwt)).then(
    () => undefined,
    (e: unknown) => e as Error,
  );
  expect(failure?.message).toMatch(/could not read Google's token signing keys: 503/);
  expect(failure?.message).not.toContain(jwt);
  expect(s.calls()).toBe(3);
});

test("Google's keys that just failed are not read again for ten seconds", async () => {
  const s = setup([() => new Response("down", { status: 503 })]);
  const jwt = bearer(token(goodPayload()));
  const first = await s.check(jwt).catch((e: Error) => e);

  s.clock.now += 9_999;
  const second = await s.check(jwt).catch((e: Error) => e);

  expect(second).toEqual(first);
  expect(s.calls()).toBe(3);
});

test("Google's keys that failed are read again once ten seconds have passed", async () => {
  const s = setup([
    () => new Response("down", { status: 503 }),
    () => new Response("down", { status: 503 }),
    () => new Response("down", { status: 503 }),
    () => Response.json({ keys: [k1.jwk] }, { headers: { "cache-control": "max-age=3600" } }),
  ]);
  const jwt = bearer(token(goodPayload()));
  await expect(s.check(jwt)).rejects.toThrow(/503/);

  s.clock.now += 10_000;

  expect(await s.check(jwt)).toBe(true);
});

import { createHmac } from "node:crypto";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import vectors from "../../../../../../../pkg/edge/edgeconformance/gate.json" with { type: "json" };

const read = (path) => readFileSync(new URL(`../${path}`, import.meta.url), "utf8");

const withoutImport = (code) => code.replace(/^import cf from 'cloudfront';\n/m, "");

const RESOLVERS = [
  ["source", withoutImport(`${read("src/resolver.js")}\n${read("src/gate.js")}`)],
  ["minified", withoutImport(read("minified/resolver.js"))],
];

function compile(program, cf) {
  return new Function("cf", "require", `${program}\nreturn handler;`)(
    cf,
    createRequire(import.meta.url),
  );
}

class KeyMissing extends Error {}

class StoreUnreachable extends Error {}

function cloudfront(entries, { failing = [] } = {}) {
  const origins = [];
  const reads = [];
  return {
    origins,
    reads,
    kvs: () => ({
      get: async (key) => {
        reads.push(key);
        if (failing.includes(key)) throw new StoreUnreachable("InternalServerException");
        if (!Object.hasOwn(entries, key)) throw new KeyMissing(`no key ${key}`);
        return entries[key];
      },
      exists: async (key) => {
        if (failing.includes(key)) throw new StoreUnreachable("InternalServerException");
        return Object.hasOwn(entries, key);
      },
    }),
    updateRequestOrigin: (origin) => origins.push(origin),
    selectRequestOriginById: () => {},
  };
}

const SLUG = "shop";

function routeOf(tier) {
  return {
    stack: `ocel.shop.${tier}`,
    slug: SLUG,
    origin: "abcdef.lambda-url.eu-west-1.on.aws",
    release: "r3f8a1c9d",
    assets: "ocel-assets.s3.eu-west-1.amazonaws.com",
    immutable: ["/_next/static/"],
    secret: "5e884898da28047151d0e56f8dc6292773603d0d",
  };
}

const TIERS = [
  ["production", { host: "shop.example.com", route: routeOf("production") }],
  ["preview", { host: "app-abc.preview.example.com", route: routeOf("preview") }],
];

function wire(value) {
  return String.fromCharCode(...new TextEncoder().encode(value));
}

function pairsOf(headerLines) {
  const pairs = [];
  for (const line of headerLines) {
    for (const pair of line.split(";")) {
      const trimmed = pair.replace(/^[ \t]+|[ \t]+$/g, "");
      if (trimmed !== "") pairs.push(trimmed);
    }
  }
  return pairs;
}

function entryOf(values) {
  return values.length === 1
    ? { value: values[0] }
    : { value: values[0], multiValue: values.map((value) => ({ value })) };
}

function eventFor({ method, host, target, headers }) {
  const [uri, query] = target.split(/\?(.*)/s, 2);
  const querystring = {};
  for (const pair of query === undefined ? [] : query.split("&")) {
    const [name, value = ""] = pair.split(/=(.*)/s, 2);
    (querystring[name] ??= []).push(value);
  }
  const wired = { host: [host] };
  const cookieLines = [];
  for (const [name, raw] of Object.entries(headers)) {
    const values = [raw].flat().map(wire);
    if (name.toLowerCase() === "cookie") cookieLines.push(...values);
    else wired[name.toLowerCase()] = values;
  }
  const cookies = {};
  for (const pair of pairsOf(cookieLines)) {
    const [name, value = ""] = pair.split(/=(.*)/s, 2);
    (cookies[name] ??= []).push(value);
  }
  const request = {
    method,
    uri,
    rawQueryString: () => query,
    querystring: Object.fromEntries(
      Object.entries(querystring).map(([name, values]) => [name, entryOf(values)]),
    ),
    headers: Object.fromEntries(
      Object.entries(wired).map(([name, values]) => [name, entryOf(values)]),
    ),
    cookies: Object.fromEntries(
      Object.entries(cookies).map(([name, values]) => [name, entryOf(values)]),
    ),
  };
  return { request };
}

function cookieHeaderOf(request) {
  const pairs = [];
  for (const [name, entry] of Object.entries(request.cookies)) {
    for (const item of entry.multiValue ?? [entry]) pairs.push(`${name}=${item.value}`);
  }
  return pairs.length === 0 ? undefined : pairs.join("; ");
}

function forwardedHeader(request, name) {
  if (name === "cookie") return cookieHeaderOf(request);
  const entry = request.headers[name];
  return entry === undefined
    ? undefined
    : (entry.multiValue ?? [entry]).map((item) => item.value).join(", ");
}

function setCookieOf(answered) {
  const entries = Object.entries(answered.cookies ?? {});
  return entries.length === 0
    ? undefined
    : entries.map(([name, c]) => `${name}=${c.value}; ${c.attributes}`).join(", ");
}

function answeredHeader(answered, name) {
  if (name === "set-cookie") return setCookieOf(answered);
  return answered.headers[name]?.value;
}

async function admit(program, tier, event, gate, { entries, options } = {}) {
  const cf = cloudfront({ [tier.host]: tier.route, [`gate/${SLUG}`]: gate, ...entries }, options);
  const answered = await compile(program, cf)(event);
  return { answered, cf };
}

function distribution(program, cf) {
  const viewerRequest = compile(program, cf);
  const cache = new Map();
  const keyOf = (request) =>
    [
      forwardedHeader(request, "host"),
      forwardedHeader(request, "x-ocel-cache-key"),
      JSON.stringify(request.querystring),
    ].join("\n");
  return {
    warm: (request, body) => cache.set(keyOf(request), { statusCode: 200, body }),
    serve: async (event) => {
      const answered = await viewerRequest(event);
      if (answered !== event.request) return answered;
      return cache.get(keyOf(answered)) ?? { statusCode: 200, body: "from the origin" };
    },
  };
}

function signedGate(example) {
  const gate = { ...vectors.gate, ...example.gate };
  const password =
    example.password ??
    (example.gate && "passwordHash" in example.gate ? undefined : vectors.password);
  if (password === undefined) return { ...gate, passwordHash: "" };
  const signature = createHmac("sha256", gate.key).update(`password\n${password}`).digest("hex");
  return { ...gate, passwordHash: `hmac-sha256$${signature}` };
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(vectors.nowSeconds * 1000);
});

afterEach(() => {
  vi.useRealTimers();
});

describe.each(RESOLVERS)("the %s resolver", (_name, program) => {
  describe("answers the gate's conformance vectors", () => {
    for (const example of vectors.cases) {
      const tier = { host: example.request.host, route: routeOf("production") };

      it(example.name, async () => {
        const gate = signedGate(example);
        const event = eventFor(example.request);
        const { answered } = await admit(program, tier, event, gate, {
          entries: { [example.request.host.toLowerCase()]: tier.route },
        });

        if (example.expect.forward) {
          expect(answered, "forwarded to the origin").toBe(event.request);
          for (const [name, want] of Object.entries(example.expect.forwardHeaders ?? {})) {
            expect(forwardedHeader(answered, name), name).toBe(want);
          }
          for (const name of example.expect.forwardHeadersAbsent ?? []) {
            expect(forwardedHeader(answered, name), name).toBeUndefined();
          }
          return;
        }

        expect(answered.statusCode).toBe(example.expect.status);
        for (const [name, want] of Object.entries(example.expect.headers ?? {})) {
          expect(answeredHeader(answered, name), name).toBe(want);
        }
        for (const name of example.expect.headersAbsent ?? []) {
          expect(answeredHeader(answered, name), name).toBeUndefined();
        }
        for (const want of example.expect.bodyContains ?? []) expect(answered.body).toContain(want);
        for (const unwanted of example.expect.bodyExcludes ?? []) {
          expect(answered.body).not.toContain(unwanted);
        }
      });
    }
  });

  describe.each(TIERS)("on a %s hostname", (_tier, tier) => {
    const refused = { method: "GET", host: tier.host, target: "/", headers: {} };
    const bypassed = {
      ...refused,
      headers: { "x-ocel-bypass": vectors.gate.bypassSecrets[0] },
    };

    it("leaves the hostname ungated until a gate record exists, and asks the store for it", async () => {
      const cf = cloudfront({ [tier.host]: tier.route });
      const event = eventFor(refused);

      const answered = await compile(program, cf)(event);

      expect(answered).toBe(event.request);
      expect(cf.reads).toEqual([tier.host, `gate/${SLUG}`]);
    });

    it("refuses with 503 and never caches it when the store cannot say whether a gate exists", async () => {
      const cf = cloudfront({ [tier.host]: tier.route }, { failing: [`gate/${SLUG}`] });

      const answered = await compile(program, cf)(eventFor(refused));

      expect(answered.statusCode).toBe(503);
      expect(answered.headers["cache-control"].value).toBe("private, no-store");
      expect(answered.headers["x-robots-tag"].value).toBe("noindex");
      expect(cf.origins).toHaveLength(0);
    });

    it("refuses with 503 when the gate record is not a gate", async () => {
      for (const record of [
        { key: "" },
        "gate",
        [],
        { key: "k", passwordHash: "m", bypassSecrets: 1, allowOptions: [] },
      ]) {
        const { answered, cf } = await admit(program, tier, eventFor(refused), record);

        expect(answered.statusCode).toBe(503);
        expect(cf.origins).toHaveLength(0);
      }
    });

    it("refuses the right password against an scrypt hash, which it has no CPU to derive", async () => {
      const basic = `Basic ${btoa(`u:${vectors.password}`)}`;
      const event = eventFor({ ...refused, headers: { authorization: basic } });

      const { answered, cf } = await admit(program, tier, event, vectors.gate);

      expect(answered.statusCode).toBe(401);
      expect(cf.origins).toHaveLength(0);
    });

    it("refuses a request with no credentials before it reaches any origin", async () => {
      const { answered, cf } = await admit(program, tier, eventFor(refused), vectors.gate);

      expect(answered.statusCode).toBe(401);
      expect(answered.headers["cache-control"].value).toBe("private, no-store");
      expect(answered.headers["x-robots-tag"].value).toBe("noindex");
      expect(cf.origins).toHaveLength(0);
    });

    it("refuses with the gate before it names the cache key a cached copy is found by", async () => {
      const { answered } = await admit(program, tier, eventFor(refused), vectors.gate);

      expect(answered.statusCode).toBe(401);
      expect(answered.headers["x-ocel-cache-key"]).toBeUndefined();
    });

    it("refuses a request for an immutable asset before it points it at the edge bucket", async () => {
      const event = eventFor({ ...refused, target: "/_next/static/chunk.js" });

      const { answered, cf } = await admit(program, tier, event, vectors.gate);

      expect(answered.statusCode).toBe(401);
      expect(cf.origins).toHaveLength(0);
    });

    it("answers an unauthenticated request with the login form though the cache holds the page", async () => {
      const cf = cloudfront({ [tier.host]: tier.route, [`gate/${SLUG}`]: vectors.gate });
      const front = distribution(program, cf);
      const warmed = await compile(program, cf)(eventFor(bypassed));
      front.warm(warmed, "the protected page");

      const admitted = await front.serve(eventFor(bypassed));
      const answered = await front.serve(eventFor(refused));

      expect(admitted.body).toBe("the protected page");
      expect(answered.statusCode).toBe(401);
      expect(answered.body).not.toContain("the protected page");
      expect(answered.headers["cache-control"].value).toBe("private, no-store");
    });

    it("strips the client's control headers before it reads the gate's verdict", async () => {
      const event = eventFor({
        ...refused,
        headers: { "x-ocel-origin-secret": "forged", "x-middleware-rewrite": "/x" },
      });

      const { answered } = await admit(program, tier, event, vectors.gate);

      expect(answered.statusCode).toBe(401);
      expect(event.request.headers["x-ocel-origin-secret"]).toBeUndefined();
    });
  });
});

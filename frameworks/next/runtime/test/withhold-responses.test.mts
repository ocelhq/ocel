import http from "node:http";
import type { AddressInfo } from "node:net";
import { expect, test } from "vitest";
import {
  isWithheldFromSharedCache,
  withholdResponsesFromSharedCache,
} from "../src/withhold-responses.mjs";

const uncacheable = "private, no-cache, no-store, max-age=0, must-revalidate";

const now = Date.parse("Mon, 12 Oct 2026 12:00:00 GMT");

test.each([
  ["an s-maxage", { "cache-control": "s-maxage=60, stale-while-revalidate=31535940" }],
  ["an s-maxage in any case", { "cache-control": "S-MaxAge=31536000" }],
  ["a max-age and no s-maxage", { "cache-control": "public, max-age=60" }],
  ["a future Expires and no Cache-Control", { expires: "Mon, 12 Oct 2026 12:01:00 GMT" }],
  [
    "a future Expires and a Cache-Control naming no lifetime",
    { "cache-control": "public", expires: "Mon, 12 Oct 2026 12:01:00 GMT" },
  ],
] as const)(
  "a response with %s is withheld from a shared cache, because nothing tags it for clearing",
  (_name, headers) => {
    expect(isWithheldFromSharedCache(new Headers(headers), now)).toBe(true);
  },
);

test.each([
  ["no cache headers", {}],
  ["a refusal", { "cache-control": uncacheable }],
  ["a max-age of zero", { "cache-control": "public, max-age=0" }],
  ["an Expires in the past", { expires: "Mon, 12 Oct 2026 11:59:00 GMT" }],
  ["an immutable max-age", { "cache-control": "public, max-age=31536000, immutable" }],
  ["an immutable s-maxage", { "cache-control": "public, s-maxage=60, immutable" }],
] as const)("a response with %s is left storable as it is", (_name, headers) => {
  expect(isWithheldFromSharedCache(new Headers(headers), now)).toBe(false);
});

test("a next start response that names s-maxage leaves the server private and unstorable", async () => {
  class TestResponse extends http.ServerResponse {}
  withholdResponsesFromSharedCache(TestResponse.prototype);
  const server = http.createServer({ ServerResponse: TestResponse }, (req, res) => {
    if (req.url === "/static")
      res.setHeader("cache-control", "public, max-age=31536000, immutable");
    else res.setHeader("cache-control", "s-maxage=60");
    res.end("x");
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
    expect((await fetch(`${base}/page`)).headers.get("cache-control")).toBe(uncacheable);
    expect((await fetch(`${base}/static`)).headers.get("cache-control")).toBe(
      "public, max-age=31536000, immutable",
    );
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});

async function cacheControlSeenFor(
  respond: (res: http.ServerResponse) => void,
): Promise<{ cacheControl: string | null; other: string | null }> {
  class TestResponse extends http.ServerResponse {}
  withholdResponsesFromSharedCache(TestResponse.prototype);
  const server = http.createServer({ ServerResponse: TestResponse }, (_req, res) => {
    respond(res);
    res.end("x");
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    const response = await fetch(`http://127.0.0.1:${(server.address() as AddressInfo).port}/`);
    return {
      cacheControl: response.headers.get("cache-control"),
      other: response.headers.get("x-other"),
    };
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
}

test("a Cache-Control passed to writeHead as an object is withheld whatever its key's case", async () => {
  for (const key of ["Cache-Control", "cache-control", "CACHE-CONTROL"]) {
    const seen = await cacheControlSeenFor((res) =>
      res.writeHead(200, { [key]: "s-maxage=60", "x-other": "kept" }),
    );
    expect(seen).toEqual({ cacheControl: uncacheable, other: "kept" });
  }
});

test("a Cache-Control passed to writeHead after a status message is withheld", async () => {
  const seen = await cacheControlSeenFor((res) =>
    res.writeHead(200, "Fine", { "Cache-Control": "s-maxage=60", "x-other": "kept" }),
  );
  expect(seen).toEqual({ cacheControl: uncacheable, other: "kept" });
});

test("a Cache-Control passed to writeHead as a flat raw array is withheld", async () => {
  const seen = await cacheControlSeenFor((res) =>
    res.writeHead(200, ["x-other", "kept", "Cache-Control", "s-maxage=60"]),
  );
  expect(seen).toEqual({ cacheControl: uncacheable, other: "kept" });
  const afterMessage = await cacheControlSeenFor((res) =>
    res.writeHead(200, "Fine", ["Cache-Control", "s-maxage=60", "x-other", "kept"]),
  );
  expect(afterMessage).toEqual({ cacheControl: uncacheable, other: "kept" });
});

test("an immutable or unstorable Cache-Control passed to writeHead is left as it is", async () => {
  const immutable = "public, max-age=31536000, immutable";
  const asObject = await cacheControlSeenFor((res) =>
    res.writeHead(200, { "cache-control": immutable }),
  );
  expect(asObject.cacheControl).toBe(immutable);
  const asArray = await cacheControlSeenFor((res) =>
    res.writeHead(200, ["cache-control", immutable]),
  );
  expect(asArray.cacheControl).toBe(immutable);
  const unstorable = await cacheControlSeenFor((res) =>
    res.writeHead(200, { "cache-control": "public, max-age=0" }),
  );
  expect(unstorable.cacheControl).toBe("public, max-age=0");
});

test("a response with a max-age and no s-maxage is withheld", async () => {
  const seen = await cacheControlSeenFor((res) =>
    res.writeHead(200, { "cache-control": "public, max-age=60", "x-other": "kept" }),
  );
  expect(seen).toEqual({ cacheControl: uncacheable, other: "kept" });
});

test("a response whose only lifetime is a future Expires is withheld, from setHeader or writeHead", async () => {
  const expires = () => new Date(Date.now() + 60_000).toUTCString();
  const staged = await cacheControlSeenFor((res) => res.setHeader("expires", expires()));
  expect(staged.cacheControl).toBe(uncacheable);
  const written = await cacheControlSeenFor((res) =>
    res.writeHead(200, ["Expires", expires(), "x-other", "kept"]),
  );
  expect(written).toEqual({ cacheControl: uncacheable, other: "kept" });
});

test("a Cache-Control staged with setHeader is still withheld when writeHead carries other headers", async () => {
  const seen = await cacheControlSeenFor((res) => {
    res.setHeader("cache-control", "s-maxage=60");
    res.writeHead(200, { "x-other": "kept" });
  });
  expect(seen).toEqual({ cacheControl: uncacheable, other: "kept" });
});

test("a Cache-Control repeated in writeHead is judged on every value it sends", async () => {
  const flat = await cacheControlSeenFor((res) =>
    res.writeHead(200, [
      "Cache-Control",
      "public",
      "Cache-Control",
      "s-maxage=60",
      "x-other",
      "kept",
    ]),
  );
  expect(flat).toEqual({ cacheControl: uncacheable, other: "kept" });
  const paired = await cacheControlSeenFor((res) =>
    res.writeHead(200, [
      ["Cache-Control", "public"],
      ["Cache-Control", "s-maxage=60"],
      ["x-other", "kept"],
    ]),
  );
  expect(paired).toEqual({ cacheControl: uncacheable, other: "kept" });
  const cased = await cacheControlSeenFor((res) =>
    res.writeHead(200, {
      "Cache-Control": "public",
      "cache-control": "s-maxage=60",
      "x-other": "kept",
    }),
  );
  expect(cased).toEqual({ cacheControl: uncacheable, other: "kept" });
});

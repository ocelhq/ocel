import { expect, it } from "vitest";

import { bypassCookie, readBypass, revalidateHeader } from "../src/bypass.mjs";

const token = "0123456789abcdef0123456789abcdef";

it("names the cookie and the header Vercel's prerender bypass uses", () => {
  expect(bypassCookie).toBe("__prerender_bypass");
  expect(revalidateHeader).toBe("x-prerender-revalidate");
});

it("bypasses the cache for a request carrying the token in the bypass cookie", () => {
  const cookie = `theme=dark; ${bypassCookie}=${token}; session=abc`;
  expect(readBypass({ token, method: "GET", cookie })).toBe("bypass");
  expect(readBypass({ token, method: "POST", cookie })).toBe("bypass");
});

it("refuses a bypass cookie that holds another token, or none", () => {
  expect(readBypass({ token, method: "GET", cookie: `${bypassCookie}=${token}x` })).toBe("none");
  expect(readBypass({ token, method: "GET", cookie: `${bypassCookie}=` })).toBe("none");
  expect(readBypass({ token, method: "GET", cookie: `x${bypassCookie}=${token}` })).toBe("none");
  expect(readBypass({ token, method: "GET", cookie: "theme=dark" })).toBe("none");
  expect(readBypass({ token, method: "GET" })).toBe("none");
});

it("takes the token from any cookie of that name", () => {
  const cookie = `${bypassCookie}=stale; ${bypassCookie}=${token}`;
  expect(readBypass({ token, method: "GET", cookie })).toBe("bypass");
});

it("refills the cache for a GET or HEAD carrying the token in the revalidate header", () => {
  expect(readBypass({ token, method: "GET", revalidate: token })).toBe("revalidate");
  expect(readBypass({ token, method: "HEAD", revalidate: token })).toBe("revalidate");
  expect(readBypass({ token, method: "POST", revalidate: token })).toBe("none");
  expect(readBypass({ token, method: "GET", revalidate: "other" })).toBe("none");
});

it("lets the bypass cookie win over the revalidate header, because a bypassed request is never stored", () => {
  const cookie = `${bypassCookie}=${token}`;
  expect(readBypass({ token, method: "GET", cookie, revalidate: token })).toBe("bypass");
});

it("bypasses nothing where the table states no token", () => {
  const cookie = `${bypassCookie}=${token}`;
  expect(readBypass({ token: undefined, method: "GET", cookie, revalidate: token })).toBe("none");
  expect(readBypass({ token: "", method: "GET", cookie: `${bypassCookie}=`, revalidate: "" })).toBe(
    "none",
  );
});

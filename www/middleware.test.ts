import { unstable_doesMiddlewareMatch } from "next/experimental/testing/server";
import { NextRequest } from "next/server";
import { describe, expect, it } from "vitest";
import { config, middleware } from "./middleware";

const matches = (url: string) => unstable_doesMiddlewareMatch({ config, nextConfig: {}, url });

describe("the go vanity import", () => {
  it("answers go get at the root and at every path beneath it", () => {
    expect(matches("/?go-get=1")).toBe(true);
    expect(matches("/internal/proto?go-get=1")).toBe(true);
  });

  it("leaves every other request to the site", () => {
    expect(matches("/docs/quick-start")).toBe(false);
    expect(matches("/docs?go-get=0")).toBe(false);
    expect(matches("/schema/0.0.0/ocel.schema.json")).toBe(false);
  });

  it("points ocel.dev at the sdk directory of the repo", async () => {
    const response = middleware(new NextRequest("https://ocel.dev/?go-get=1"));
    expect(response.headers.get("content-type")).toBe("text/html; charset=utf-8");
    expect(await response.text()).toContain(
      '<meta name="go-import" content="ocel.dev git https://github.com/ocelhq/ocel sdk">',
    );
  });
});

describe("the root", () => {
  it("is matched", () => {
    expect(matches("/")).toBe(true);
  });

  it("redirects to the docs for now", () => {
    const response = middleware(new NextRequest("https://ocel.dev/"));
    expect(response.status).toBe(307);
    expect(response.headers.get("location")).toBe("https://ocel.dev/docs");
  });
});

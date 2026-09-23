import { unstable_doesMiddlewareMatch } from "next/experimental/testing/server";
import { describe, expect, it } from "vitest";
import { config, proxy } from "./proxy";

const matches = (url: string) => unstable_doesMiddlewareMatch({ config, nextConfig: {}, url });

describe("the go vanity import", () => {
  it("answers go get at the root and at every path beneath it", () => {
    expect(matches("/?go-get=1")).toBe(true);
    expect(matches("/internal/proto?go-get=1")).toBe(true);
  });

  it("leaves every other request to the site", () => {
    expect(matches("/")).toBe(false);
    expect(matches("/docs/quick-start")).toBe(false);
    expect(matches("/?go-get=0")).toBe(false);
    expect(matches("/schema/0.0.0/ocel.schema.json")).toBe(false);
  });

  it("points ocel.dev at the sdk directory of the repo", async () => {
    const response = proxy();
    expect(response.headers.get("content-type")).toBe("text/html; charset=utf-8");
    expect(await response.text()).toContain(
      '<meta name="go-import" content="ocel.dev git https://github.com/ocelhq/ocel sdk">',
    );
  });
});

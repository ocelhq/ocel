import { describe, expect, it } from "vitest";
import { addCacheHandlers } from "../src/naming.mjs";

describe("addCacheHandlers", () => {
  it("points the cache handler and the default and remote use-cache handlers at the runtime dir", () => {
    expect(addCacheHandlers({}, "/ocel/next")).toEqual({
      cacheHandler: "/ocel/next/cache-handler.cjs",
      cacheHandlers: {
        default: "/ocel/next/use-cache-default.cjs",
        remote: "/ocel/next/use-cache-remote.cjs",
      },
    });
  });

  it("keeps the app's own named cache handlers beside ocel's", () => {
    const config = { cacheHandlers: { custom: "/app/custom.cjs", default: "/app/own.cjs" } };

    expect(addCacheHandlers(config, "/ocel/next").cacheHandlers).toEqual({
      custom: "/app/custom.cjs",
      default: "/ocel/next/use-cache-default.cjs",
      remote: "/ocel/next/use-cache-remote.cjs",
    });
  });
});

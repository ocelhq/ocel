import { expect, test } from "vitest";
import { groupPrerenders } from "../src/cache-seed.mjs";

test("seeds the root page and a page named index under the keys the cache handler reads them by", () => {
  const groups = groupPrerenders([
    { groupId: 1, pathname: "/" },
    { groupId: 1, pathname: "/index.rsc" },
    { groupId: 2, pathname: "/index" },
    { groupId: 2, pathname: "/index/index.rsc" },
  ]);

  expect(groups.map((group) => group.entryKey)).toEqual(["index", "index/index"]);
});

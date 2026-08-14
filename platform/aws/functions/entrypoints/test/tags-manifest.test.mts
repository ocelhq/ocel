import { afterEach, expect, test } from "vitest";

import { mirrorTag, mirrorTagsInto } from "../src/next/tags-manifest.mjs";

afterEach(() => {
  mirrorTagsInto(null);
});

test("mirrors nothing while no manifest is registered", () => {
  expect(() => mirrorTag("posts", { expired: 5 })).not.toThrow();
});

test("carries a record into the registered manifest", () => {
  const manifest = new Map();
  mirrorTagsInto(manifest);

  mirrorTag("posts", { stale: 5, expired: 9 });

  expect(manifest.get("posts")).toEqual({ stale: 5, expired: 9 });
});

test("moves each mark forward only", () => {
  const manifest = new Map([["posts", { stale: 7, expired: 20 }]]);
  mirrorTagsInto(manifest);

  mirrorTag("posts", { stale: 3, expired: 30 });

  expect(manifest.get("posts")).toEqual({ stale: 7, expired: 30 });
});

import { afterEach, describe, expect, it } from "vitest";

import { cacheTag, revalidatePath, revalidateTag } from "./index.js";

const hostKey = Symbol.for("ocel.cache.host");

interface Recorded {
  tagged: string[][];
  revalidatedTags: { tag: string; mode: string }[];
  revalidatedPaths: string[];
}

function installHost(): Recorded {
  const recorded: Recorded = { tagged: [], revalidatedTags: [], revalidatedPaths: [] };
  (globalThis as Record<symbol, unknown>)[hostKey] = {
    tag: (tags: string[]) => recorded.tagged.push(tags),
    revalidateTag: (tag: string, mode: string) => recorded.revalidatedTags.push({ tag, mode }),
    revalidatePath: (path: string) => recorded.revalidatedPaths.push(path),
  };
  return recorded;
}

afterEach(() => {
  delete (globalThis as Record<symbol, unknown>)[hostKey];
});

describe("outside a runtime that caches", () => {
  it("tags nothing and revalidates nothing, so an app runs unchanged in dev and in tests", () => {
    expect(() => cacheTag("posts")).not.toThrow();
    expect(() => revalidateTag("posts")).not.toThrow();
    expect(() => revalidatePath("/posts")).not.toThrow();
  });
});

describe("cacheTag", () => {
  it("tags the response being answered with every tag it is given", () => {
    const recorded = installHost();
    cacheTag("posts", "post:1");
    expect(recorded.tagged).toEqual([["posts", "post:1"]]);
  });

  it("refuses an empty tag, which no purge could name", () => {
    installHost();
    expect(() => cacheTag("")).toThrow(/tag/);
  });

  it("refuses a tag that is not text", () => {
    installHost();
    expect(() => cacheTag(1 as unknown as string)).toThrow(/tag/);
  });
});

describe("revalidateTag", () => {
  it("expires everything the tag names at once when it is given no profile", () => {
    const recorded = installHost();
    revalidateTag("posts");
    expect(recorded.revalidatedTags).toEqual([{ tag: "posts", mode: "purge" }]);
  });

  it("serves what the tag names stale while it refreshes, when the profile is max", () => {
    const recorded = installHost();
    revalidateTag("posts", "max");
    expect(recorded.revalidatedTags).toEqual([{ tag: "posts", mode: "invalidate" }]);
  });

  it("refuses a profile it does not know", () => {
    installHost();
    expect(() => revalidateTag("posts", "hours" as "max")).toThrow(/profile/);
  });
});

describe("revalidatePath", () => {
  it("revalidates the page at a path", () => {
    const recorded = installHost();
    revalidatePath("/posts/1");
    expect(recorded.revalidatedPaths).toEqual(["/posts/1"]);
  });

  it("refuses a path that does not start with a slash or carries a query", () => {
    installHost();
    expect(() => revalidatePath("posts")).toThrow(/path/);
    expect(() => revalidatePath("/posts?page=2")).toThrow(/path/);
  });
});

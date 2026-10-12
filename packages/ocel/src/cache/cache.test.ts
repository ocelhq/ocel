import { afterEach, describe, expect, it } from "vitest";

import { cacheTag, revalidatePath, revalidateTag } from "./index.js";

const hostKey = Symbol.for("ocel.cache.host");

interface Recorded {
  tagged: string[][];
  revalidatedTags: { tag: string; mode: string }[];
  revalidatedPaths: { path: string; mode: string }[];
}

function installHost(): Recorded {
  const recorded: Recorded = { tagged: [], revalidatedTags: [], revalidatedPaths: [] };
  (globalThis as Record<symbol, unknown>)[hostKey] = {
    tag: (tags: string[]) => recorded.tagged.push(tags),
    revalidateTag: (tag: string, mode: string) => recorded.revalidatedTags.push({ tag, mode }),
    revalidatePath: (path: string, mode: string) => recorded.revalidatedPaths.push({ path, mode }),
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
  it("marks everything the tag names stale at once, to be served while a fresh copy is made", () => {
    const recorded = installHost();
    revalidateTag("posts");
    expect(recorded.revalidatedTags).toEqual([{ tag: "posts", mode: "invalidate" }]);
  });

  it("purges what the tag names when it is given an expiry of 0", () => {
    const recorded = installHost();
    revalidateTag("posts", { expire: 0 });
    expect(recorded.revalidatedTags).toEqual([{ tag: "posts", mode: "purge" }]);
  });

  it("invalidates when it is given options that name no expiry", () => {
    const recorded = installHost();
    revalidateTag("posts", {});
    expect(recorded.revalidatedTags).toEqual([{ tag: "posts", mode: "invalidate" }]);
  });

  it('refuses an expiry it does not know, which Next\'s profile string "max" is among', () => {
    installHost();
    expect(() => revalidateTag("posts", { expire: 60 } as unknown as { expire: 0 })).toThrow(
      /expire/,
    );
    expect(() => revalidateTag("posts", "max" as unknown as { expire: 0 })).toThrow(/expire/);
  });

  it("refuses an empty tag", () => {
    installHost();
    expect(() => revalidateTag("")).toThrow(/tag/);
  });
});

describe("revalidatePath", () => {
  it("marks the page at a path stale, to be served while a fresh copy is made", () => {
    const recorded = installHost();
    revalidatePath("/posts/1");
    expect(recorded.revalidatedPaths).toEqual([{ path: "/posts/1", mode: "invalidate" }]);
  });

  it("purges the page at a path when it is given an expiry of 0", () => {
    const recorded = installHost();
    revalidatePath("/posts/1", { expire: 0 });
    expect(recorded.revalidatedPaths).toEqual([{ path: "/posts/1", mode: "purge" }]);
  });

  it("refuses a path that does not start with a slash or carries a query", () => {
    installHost();
    expect(() => revalidatePath("posts")).toThrow(/path/);
    expect(() => revalidatePath("/posts?page=2")).toThrow(/path/);
  });

  it("refuses an expiry it does not know", () => {
    installHost();
    expect(() => revalidatePath("/posts", { expire: 5 } as unknown as { expire: 0 })).toThrow(
      /expire/,
    );
  });
});

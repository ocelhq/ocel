import { tagNamespace, tagSortKey } from "@platform/aws-runtime/tag-index";
import { describe, expect, it } from "vitest";

import { raisesOf, type StreamRecord } from "../src/records.mjs";

const BUILD = "acme/prod/web/r3f8a1c9d";

let nextSequence = 0;

function tagRecord(
  tag: string,
  attrs: Record<string, { S?: string; N?: string }>,
  namespace = tagNamespace({ project: "acme", env: "prod", app: "web", release: "r3f8a1c9d" })!,
  writtenAt = 1_000,
): StreamRecord {
  return {
    dynamodb: {
      SequenceNumber: String(++nextSequence),
      NewImage: {
        pk: { S: `${namespace}${tag}` },
        sk: { S: "#META" },
        gsi1pk: { S: namespace },
        gsi1sk: { S: tagSortKey(writtenAt) },
        tag: { S: tag },
        ...attrs,
      },
    },
  };
}

describe("raisesOf", () => {
  it("groups a batch by the build each record belongs to", () => {
    const other = tagNamespace({
      project: "acme",
      env: "prod",
      app: "admin",
      release: "rbbbbbbbb",
    })!;
    const raises = raisesOf([
      tagRecord("cart", { expired: { N: "100" } }),
      tagRecord("home", { stale: { N: "200" } }),
      tagRecord("cart", { expired: { N: "300" } }, other),
    ]);

    expect([...raises.keys()].sort()).toEqual(["acme/prod/admin/rbbbbbbbb", BUILD]);
    expect(raises.get(BUILD)!.tags).toEqual(["cart", "home"]);
    expect(raises.get("acme/prod/admin/rbbbbbbbb")!.tags).toEqual(["cart"]);
  });

  it("carries the storage coordinate of the build that raised it", () => {
    const raises = raisesOf([tagRecord("cart", { expired: { N: "100" } })]);

    expect(raises.get(BUILD)!.coordinate).toEqual({
      project: "acme",
      env: "prod",
      app: "web",
      release: "r3f8a1c9d",
    });
  });

  it("names a tag once however many records raised it", () => {
    const raises = raisesOf([
      tagRecord("cart", { expired: { N: "300" } }),
      tagRecord("cart", { stale: { N: "50" } }),
    ]);

    expect(raises.get(BUILD)!.tags).toEqual(["cart"]);
    expect(raises.get(BUILD)!.sequenceNumbers).toHaveLength(2);
  });

  it("reads a tag expired at once as a purge raised when it was expired", () => {
    const raises = raisesOf([tagRecord("cart", { expired: { N: "300" } })]);

    expect(raises.get(BUILD)!.entries).toEqual([{ tag: "cart", mode: "purge", raisedAt: 300 }]);
  });

  it("reads a tag only marked stale as an invalidate raised when it went stale", () => {
    const raises = raisesOf([tagRecord("home", { stale: { N: "200" } })]);

    expect(raises.get(BUILD)!.entries).toEqual([
      { tag: "home", mode: "invalidate", raisedAt: 200 },
    ]);
  });

  it("reads a tag stale now and expiring later as an invalidate, raised when it went stale", () => {
    const raises = raisesOf([tagRecord("home", { stale: { N: "200" }, expired: { N: "60200" } })]);

    expect(raises.get(BUILD)!.entries).toEqual([
      { tag: "home", mode: "invalidate", raisedAt: 200 },
    ]);
  });

  it("reads a tag stale and expired together as a purge", () => {
    const raises = raisesOf([tagRecord("home", { stale: { N: "200" }, expired: { N: "200" } })]);

    expect(raises.get(BUILD)!.entries).toEqual([{ tag: "home", mode: "purge", raisedAt: 200 }]);
  });

  it("reads a tag gone stale and later expired as a purge raised when it expired", () => {
    const raises = raisesOf([
      tagRecord("home", { stale: { N: "200" }, expired: { N: "600" } }, undefined, 600),
    ]);

    expect(raises.get(BUILD)!.entries).toEqual([{ tag: "home", mode: "purge", raisedAt: 600 }]);
  });

  it("reads a tag expired and later gone stale as an invalidate raised when it went stale", () => {
    const raises = raisesOf([
      tagRecord("home", { stale: { N: "600" }, expired: { N: "200" } }, undefined, 600),
    ]);

    expect(raises.get(BUILD)!.entries).toEqual([
      { tag: "home", mode: "invalidate", raisedAt: 600 },
    ]);
  });

  it("keeps both a purge and a later invalidate of a tag raised twice", () => {
    const raises = raisesOf([
      tagRecord("cart", { expired: { N: "300" } }, undefined, 300),
      tagRecord("cart", { stale: { N: "500" }, expired: { N: "300" } }, undefined, 500),
    ]);

    expect(raises.get(BUILD)!.entries).toEqual([
      { tag: "cart", mode: "purge", raisedAt: 300 },
      { tag: "cart", mode: "invalidate", raisedAt: 500 },
    ]);
  });

  it("drops an invalidate of a tag that a later purge of it covers", () => {
    const raises = raisesOf([
      tagRecord("cart", { stale: { N: "300" } }, undefined, 300),
      tagRecord("cart", { stale: { N: "300" }, expired: { N: "500" } }, undefined, 500),
    ]);

    expect(raises.get(BUILD)!.entries).toEqual([{ tag: "cart", mode: "purge", raisedAt: 500 }]);
  });

  it("keeps the later of two raises of a tag in one mode", () => {
    const raises = raisesOf([
      tagRecord("cart", { stale: { N: "500" } }, undefined, 500),
      tagRecord("cart", { stale: { N: "300" } }, undefined, 300),
    ]);

    expect(raises.get(BUILD)!.entries).toEqual([
      { tag: "cart", mode: "invalidate", raisedAt: 500 },
    ]);
  });

  it("derives the build from gsi1pk, not from a pk a tag can forge", () => {
    const raises = raisesOf([
      tagRecord("evil#PROJECT#other#STACK#prod--app--r00000000#TAG#", { expired: { N: "1" } }),
    ]);
    expect([...raises.keys()]).toEqual([BUILD]);
  });

  it("acts on no partition that only looks like a tag namespace", () => {
    for (const namespace of [
      "PROJECT#acme#CLASS#production#TAG#",
      "PROJECT#acme#STACK#prod--web#TAG#",
      "TAG#prod#acme#web#r3f8a1c9d#isr#",
    ]) {
      expect(raisesOf([tagRecord("cart", { expired: { N: "1" } }, namespace)]).size).toBe(0);
    }
  });

  it("drops a record with no watermark at all", () => {
    expect(raisesOf([tagRecord("cart", {})]).size).toBe(0);
    expect(raisesOf([tagRecord("cart", { expired: { N: "nope" } })]).size).toBe(0);
    expect(raisesOf([tagRecord("cart", { expired: { N: "-1" } })]).size).toBe(0);
  });

  it("ignores a record with no new image, which a REMOVE produces", () => {
    expect(raisesOf([{ dynamodb: {} }, {}]).size).toBe(0);
  });
});

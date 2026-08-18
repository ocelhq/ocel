import { tagNamespace } from "@framework/next-cache";
import { describe, expect, it } from "vitest";

import { coordinateOf, raisesOf, type StreamRecord } from "../src/records.mjs";

const PREFIX = "prod/acme/web/r3f8a1c9d/isr";

let nextSequence = 0;

function tagRecord(
  tag: string,
  attrs: Record<string, { S?: string; N?: string }>,
  namespace = tagNamespace(PREFIX)!,
): StreamRecord {
  return {
    dynamodb: {
      SequenceNumber: String(++nextSequence),
      NewImage: {
        pk: { S: `${namespace}${tag}` },
        sk: { S: "#META" },
        gsi1pk: { S: namespace },
        gsi1sk: { S: "000000001700000" },
        tag: { S: tag },
        ...attrs,
      },
    },
  };
}

describe("raisesOf", () => {
  it("groups a batch by the build each record belongs to", () => {
    const other = tagNamespace("prod/acme/admin/rbbbbbbbb/isr")!;
    const raises = raisesOf([
      tagRecord("cart", { expired: { N: "100" } }),
      tagRecord("home", { stale: { N: "200" } }),
      tagRecord("cart", { expired: { N: "300" } }, other),
    ]);

    expect([...raises.keys()].sort()).toEqual(["prod/acme/admin/rbbbbbbbb/isr", PREFIX]);
    expect(raises.get(PREFIX)!.tags).toEqual(["cart", "home"]);
    expect(raises.get("prod/acme/admin/rbbbbbbbb/isr")!.tags).toEqual(["cart"]);
  });

  it("names a tag once however many records raised it", () => {
    const raises = raisesOf([
      tagRecord("cart", { expired: { N: "300" } }),
      tagRecord("cart", { stale: { N: "50" } }),
    ]);

    expect(raises.get(PREFIX)!.tags).toEqual(["cart"]);
    expect(raises.get(PREFIX)!.sequenceNumbers).toHaveLength(2);
  });

  it("derives the build from gsi1pk, not from a pk a tag can forge", () => {
    const raises = raisesOf([
      tagRecord("evil#PROJECT#other#STACK#prod--app--r00000000#TAG#", { expired: { N: "1" } }),
    ]);
    expect([...raises.keys()]).toEqual([PREFIX]);
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

  it("drops a record carrying no watermark at all", () => {
    expect(raisesOf([tagRecord("cart", {})]).size).toBe(0);
    expect(raisesOf([tagRecord("cart", { expired: { N: "nope" } })]).size).toBe(0);
    expect(raisesOf([tagRecord("cart", { expired: { N: "-1" } })]).size).toBe(0);
  });

  it("ignores a record with no new image, which a REMOVE carries", () => {
    expect(raisesOf([{ dynamodb: {} }, {}]).size).toBe(0);
  });
});

describe("coordinateOf", () => {
  it("reads the project the ledger is scoped by and the release the tags are prefixed with", () => {
    expect(coordinateOf(PREFIX)).toEqual({ project: "acme", release: "r3f8a1c9d" });
  });

  it("reads nothing from a prefix that is not one", () => {
    expect(coordinateOf("prod/acme/web")).toBeNull();
  });
});

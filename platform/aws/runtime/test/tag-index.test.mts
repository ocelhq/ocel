import { describe, expect, it } from "vitest";

import {
  isGuardRejection,
  isrPrefixOf,
  tagNamespace,
  tagRecordUpdate,
  tagSortKey,
} from "../src/next/tag-index.mjs";

const update = (tag: string, record: { stale?: number; expired?: number; writtenAt: number }) =>
  tagRecordUpdate("state", "PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#", tag, record);

describe("tagSortKey", () => {
  it("pads to a fixed width so string order matches numeric order", () => {
    expect(tagSortKey(1700)).toBe("000000000001700");
    expect(tagSortKey(900) < tagSortKey(1700)).toBe(true);
  });

  it("rounds, since a fractional clock reading neither pads nor orders", () => {
    expect(tagSortKey(1700.6)).toBe("000000000001701");
  });
});

describe("tagRecordUpdate", () => {
  it("addresses the record and guards the advancing watermark", () => {
    expect(update("products", { expired: 700, writtenAt: 1700 })).toEqual({
      TableName: "state",
      Key: {
        pk: { S: "PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#products" },
        sk: { S: "#META" },
      },
      ConditionExpression: "attribute_not_exists(expired) OR expired < :expired",
      UpdateExpression: "SET tag = :tag, gsi1pk = :ns, gsi1sk = :writtenAt, expired = :expired",
      ExpressionAttributeValues: {
        ":tag": { S: "products" },
        ":ns": { S: "PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#" },
        ":writtenAt": { S: "000000000001700" },
        ":expired": { N: "700" },
      },
    });
  });

  it("guards a stale-only write on stale and leaves expiry unwritten", () => {
    const input = update("products", { stale: 700, writtenAt: 1700 });
    expect(input.ConditionExpression).toBe("attribute_not_exists(stale) OR stale < :stale");
    expect(input.UpdateExpression).toBe(
      "SET tag = :tag, gsi1pk = :ns, gsi1sk = :writtenAt, stale = :stale",
    );
    expect(input.ExpressionAttributeValues).not.toHaveProperty(":expired");
  });
});

describe("isGuardRejection", () => {
  it("recognises the rejection both tiers take as the common path", () => {
    expect(isGuardRejection({ name: "ConditionalCheckFailedException" })).toBe(true);
    expect(isGuardRejection(new Error("dynamo is down"))).toBe(false);
    expect(isGuardRejection(undefined)).toBe(false);
  });
});

const SEGMENT = "abcdefghijklmnopqrstuvwxyz0123456789-";
const HEX = "0123456789abcdef";

function slug(seed: number): string {
  let out = "";
  let n = seed;
  for (let i = 0; i < 1 + (seed % 12); i++) {
    out += SEGMENT[n % SEGMENT.length];
    n = (n * 1103515245 + 12345) >>> 8;
  }
  return `p${out.replaceAll("--", "-x")}z`;
}

function release(seed: number): string {
  let out = "r";
  let n = seed;
  for (let i = 0; i < 8; i++) {
    out += HEX[n % HEX.length];
    n = (n * 1103515245 + 12345) >>> 8;
  }
  return out;
}

describe("isrPrefixOf", () => {
  it("round-trips every prefix tagNamespace can be given", () => {
    for (let seed = 0; seed < 512; seed++) {
      const prefix = [
        ...[seed, seed * 7 + 1, seed * 13 + 2].map(slug),
        release(seed * 31 + 3),
        "isr",
      ].join("/");
      const namespace = tagNamespace(prefix);
      expect(namespace).not.toBeNull();
      expect(isrPrefixOf(namespace!)).toBe(prefix);
    }
  });

  it("round-trips the shape the deploy actually produces", () => {
    expect(tagNamespace("prod/acme/web/r3f8a1c9d/isr")).toBe(
      "PROJECT#acme#STACK#prod--web--r3f8a1c9d#TAG#",
    );
    expect(isrPrefixOf("PROJECT#acme#STACK#prod--web--r3f8a1c9d#TAG#")).toBe(
      "prod/acme/web/r3f8a1c9d/isr",
    );
  });

  it("refuses a prefix that is not one release's ISR objects", () => {
    expect(tagNamespace("prod/acme/web/r3f8a1c9d")).toBeNull();
    expect(tagNamespace("prod/acme/web/r3f8a1c9d/assets")).toBeNull();
    expect(tagNamespace("prod/acme/web/r3f8a1c9d/isr/cache")).toBeNull();
    expect(tagNamespace("prod/acme//r3f8a1c9d/isr")).toBeNull();
    expect(tagNamespace("")).toBeNull();
  });

  it("refuses a field that would forge a stack boundary or a key token", () => {
    expect(tagNamespace("prod/acme/web--admin/r3f8a1c9d/isr")).toBeNull();
    expect(tagNamespace("prod/acme/web#TAG#x/r3f8a1c9d/isr")).toBeNull();
  });

  it("refuses a partition that is not a tag namespace", () => {
    expect(isrPrefixOf("UPLOAD#abc")).toBeNull();
    expect(isrPrefixOf("")).toBeNull();
    expect(isrPrefixOf("TAG#")).toBeNull();
    expect(isrPrefixOf("PROJECT#acme")).toBeNull();
    expect(isrPrefixOf("PROJECT#acme#CLASS#production")).toBeNull();
    expect(isrPrefixOf("PROJECT#acme#STACK#prod--web--r3f8a1c9d")).toBeNull();
  });

  it("refuses a namespace that is not exactly the release's stack", () => {
    expect(isrPrefixOf("PROJECT#acme#STACK#prod--web#TAG#")).toBeNull();
    expect(isrPrefixOf("PROJECT#acme#STACK#prod--web--r3f8a1c9d--extra#TAG#")).toBeNull();
    expect(isrPrefixOf("PROJECT##STACK#prod--web--r3f8a1c9d#TAG#")).toBeNull();
    expect(isrPrefixOf("PROJECT#acme#STACK#--web--r3f8a1c9d#TAG#")).toBeNull();
  });

  it("refuses a whole tag partition key, which contains the tag as well", () => {
    expect(isrPrefixOf(`${tagNamespace("prod/acme/web/r3f8a1c9d/isr")!}cart#42`)).toBeNull();
    expect(isrPrefixOf(`${tagNamespace("prod/acme/web/r3f8a1c9d/isr")!}cart`)).toBeNull();
  });
});

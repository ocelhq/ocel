import { describe, expect, it } from "vitest";

import {
  isGuardRejection,
  parseTagNamespace,
  tagNamespace,
  tagRecordUpdate,
  tagSortKey,
} from "../src/cache/tag-index.mjs";

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

const acme = { project: "acme", env: "prod", app: "web", release: "r3f8a1c9d" };

describe("parseTagNamespace", () => {
  it("round-trips every coordinate tagNamespace can be given", () => {
    for (let seed = 0; seed < 512; seed++) {
      const [project, env, app] = [seed, seed * 7 + 1, seed * 13 + 2].map(slug);
      const coordinate = {
        project: project!,
        env: env!,
        app: app!,
        release: release(seed * 31 + 3),
      };
      const namespace = tagNamespace(coordinate);
      expect(namespace).not.toBeNull();
      expect(parseTagNamespace(namespace!)).toEqual(coordinate);
    }
  });

  it("round-trips the shape the deploy actually produces", () => {
    expect(tagNamespace(acme)).toBe("PROJECT#acme#STACK#prod--web--r3f8a1c9d#TAG#");
    expect(parseTagNamespace("PROJECT#acme#STACK#prod--web--r3f8a1c9d#TAG#")).toEqual(acme);
  });

  it("refuses a coordinate that is not one release", () => {
    expect(tagNamespace({ ...acme, release: "r3f8" })).toBeNull();
    expect(tagNamespace({ ...acme, env: "" })).toBeNull();
  });

  it("refuses a field that would forge a stack boundary or a key token", () => {
    expect(tagNamespace({ ...acme, app: "web--admin" })).toBeNull();
    expect(tagNamespace({ ...acme, app: "web#TAG#x" })).toBeNull();
  });

  it("refuses a partition that is not a tag namespace", () => {
    expect(parseTagNamespace("UPLOAD#abc")).toBeNull();
    expect(parseTagNamespace("")).toBeNull();
    expect(parseTagNamespace("TAG#")).toBeNull();
    expect(parseTagNamespace("PROJECT#acme")).toBeNull();
    expect(parseTagNamespace("PROJECT#acme#CLASS#production")).toBeNull();
    expect(parseTagNamespace("PROJECT#acme#STACK#prod--web--r3f8a1c9d")).toBeNull();
  });

  it("refuses a namespace that is not exactly the release's stack", () => {
    expect(parseTagNamespace("PROJECT#acme#STACK#prod--web#TAG#")).toBeNull();
    expect(parseTagNamespace("PROJECT#acme#STACK#prod--web--r3f8a1c9d--extra#TAG#")).toBeNull();
    expect(parseTagNamespace("PROJECT##STACK#prod--web--r3f8a1c9d#TAG#")).toBeNull();
    expect(parseTagNamespace("PROJECT#acme#STACK#--web--r3f8a1c9d#TAG#")).toBeNull();
  });

  it("refuses a whole tag partition key, which contains the tag as well", () => {
    expect(parseTagNamespace(`${tagNamespace(acme)!}cart#42`)).toBeNull();
    expect(parseTagNamespace(`${tagNamespace(acme)!}cart`)).toBeNull();
  });
});

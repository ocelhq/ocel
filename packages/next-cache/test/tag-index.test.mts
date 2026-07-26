import { describe, expect, it } from "vitest";

import { isGuardRejection, tagRecordUpdate, tagSortKey } from "../src/index.mjs";

// A reader of the index cannot tell which tier wrote a row, so these assertions
// are the contract between them: the Lambda builds this update through the AWS
// SDK and the worker signs it as a raw request, and a difference in either the
// bytes or the guard corrupts the index rather than one writer.

const update = (tag: string, record: { stale?: number; expired?: number; writtenAt: number }) =>
  tagRecordUpdate("state", "TAG#prod#proj#app#BID#", tag, record);

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
      Key: { pk: { S: "TAG#prod#proj#app#BID#products" }, sk: { S: "#META" } },
      ConditionExpression: "attribute_not_exists(expired) OR expired < :expired",
      UpdateExpression:
        "SET tag = :tag, gsi1pk = :ns, gsi1sk = :writtenAt, expired = :expired",
      ExpressionAttributeValues: {
        ":tag": { S: "products" },
        ":ns": { S: "TAG#prod#proj#app#BID#" },
        ":writtenAt": { S: "000000000001700" },
        ":expired": { N: "700" },
      },
    });
  });

  // Writing an absent field as 0 would clobber a value another instance set and,
  // the guard being a strict `<`, wedge the record against its own zero.
  it("guards a stale-only write on stale and leaves expiry unwritten", () => {
    const input = update("products", { stale: 700, writtenAt: 1700 });
    expect(input.ConditionExpression).toBe(
      "attribute_not_exists(stale) OR stale < :stale",
    );
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

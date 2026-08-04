import { tagNamespace } from "@ocel/next-cache";
import { describe, expect, it } from "vitest";

import { raisesOf, type StreamRecord } from "../src/records.mjs";

const PREFIX = "prod/acme/web/BUILD1";

let nextSequence = 0;

function tagRecord(
  tag: string,
  attrs: Record<string, { S?: string; N?: string }>,
  namespace = tagNamespace(PREFIX),
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
    const other = tagNamespace("prod/acme/admin/BUILD2");
    const raises = raisesOf([
      tagRecord("cart", { expired: { N: "100" } }),
      tagRecord("home", { stale: { N: "200" } }),
      tagRecord("cart", { expired: { N: "300" } }, other),
    ]);

    expect([...raises.keys()].sort()).toEqual(["prod/acme/admin/BUILD2", PREFIX]);
    expect(raises.get(PREFIX)!.records.get("cart")).toEqual({ stale: undefined, expired: 100 });
    expect(raises.get(PREFIX)!.records.get("home")).toEqual({ stale: 200, expired: undefined });
  });

  // A build that cannot be published is reported back to the event source as
  // the records that carried it, so its batch-mates are acknowledged.
  it("remembers which records each build's raise was carried by", () => {
    const other = tagNamespace("prod/acme/admin/BUILD2");
    const raises = raisesOf([
      tagRecord("cart", { expired: { N: "100" } }),
      tagRecord("cart", { expired: { N: "300" } }, other),
      tagRecord("home", { stale: { N: "200" } }),
    ]);

    const [cart, home] = raises.get(PREFIX)!.sequenceNumbers;
    expect(raises.get("prod/acme/admin/BUILD2")!.sequenceNumbers).toEqual([
      String(Number(cart) + 1),
    ]);
    expect(home).toBe(String(Number(cart) + 2));
  });

  it("keeps the later watermark when a batch carries a tag twice", () => {
    const raises = raisesOf([
      tagRecord("cart", { expired: { N: "300" } }),
      tagRecord("cart", { expired: { N: "100" }, stale: { N: "50" } }),
    ]);
    expect(raises.get(PREFIX)!.records.get("cart")).toEqual({ stale: 50, expired: 300 });
  });

  it("derives the build from gsi1pk, not from a pk a tag can forge", () => {
    // The tag is user input and freely contains "#": splitting pk would read
    // this record as belonging to some other build entirely.
    const raises = raisesOf([tagRecord("evil#prod#other#app#B9", { expired: { N: "1" } })]);
    expect([...raises.keys()]).toEqual([PREFIX]);
  });

  it("acts on nothing that is not a tag record", () => {
    // An upload session: same table, same sort key, and an HMAC secret in it.
    const session: StreamRecord = {
      dynamodb: {
        NewImage: {
          pk: { S: "UPLOAD#abc" },
          sk: { S: "#META" },
          tag: { S: "cart" },
          expired: { N: "100" },
        },
      },
    };
    expect(raisesOf([session]).size).toBe(0);
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

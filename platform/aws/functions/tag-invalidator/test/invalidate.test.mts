import { beforeEach, expect, it, vi } from "vitest";

import { invalidateAll } from "../src/invalidate.mjs";
import type { Raises } from "../src/records.mjs";
import { pathsPerInvalidation } from "../src/tags.mjs";
import { bootstrapPartition, targetsSortKey } from "../src/targets.mjs";

const TABLE = "ocel-state";
const CLASS = "production";
const PREFIX = "prod/acme/web/r0a1b2c3d/isr";
const RELEASE = "r0a1b2c3d";
const PROJECT = "EDGELEDGER#production/acme";

class FakeDynamo {
  reads: any[] = [];
  fail: Error | null = null;
  constructor(public items = new Map<string, string[]>()) {}

  async send(command: any): Promise<any> {
    this.reads.push(command.input);
    if (this.fail !== null) throw this.fail;
    const held = this.items.get(command.input.Key.pk.S);
    return held === undefined ? {} : { Item: { distributions: { SS: held } } };
  }
}

class FakeCloudFront {
  calls: any[] = [];
  fail: Error | null = null;
  refuse = new Map<string, Error>();

  async send(command: any): Promise<any> {
    this.calls.push(command.input);
    if (this.fail !== null) throw this.fail;
    const refusal = this.refuse.get(command.input.DistributionId);
    if (refusal !== undefined) throw refusal;
    return { Invalidation: { Id: `I${this.calls.length}` } };
  }
}

const commands = {
  GetItemCommand: class {
    constructor(public input: any) {}
  },
  CreateInvalidationCommand: class {
    constructor(public input: any) {}
  },
} as any;

function named(name: string): Error {
  const error = new Error(name);
  error.name = name;
  return error;
}

function raises(tags: string[], prefix = PREFIX): Raises {
  return new Map([[prefix, { tags, sequenceNumbers: ["seq-1"] }]]);
}

function invalidator(dynamo: FakeDynamo, cloudfront: FakeCloudFront) {
  return {
    cloudfront,
    dynamo,
    commands,
    table: TABLE,
    bootstrapClass: CLASS,
    sleep: async () => {},
  };
}

let dynamo: FakeDynamo;
let cloudfront: FakeCloudFront;

beforeEach(() => {
  dynamo = new FakeDynamo(new Map([[PROJECT, ["E1PROD", "E2SECOND"]]]));
  cloudfront = new FakeCloudFront();
});

it("invalidates every distribution the ledger names for the project", async () => {
  const failed = await invalidateAll(invalidator(dynamo, cloudfront), raises(["products"]));

  expect(failed).toEqual([]);
  expect(dynamo.reads).toEqual([
    {
      TableName: TABLE,
      ConsistentRead: true,
      Key: { pk: { S: bootstrapPartition(CLASS) }, sk: { S: targetsSortKey } },
    },
    {
      TableName: TABLE,
      ConsistentRead: true,
      Key: { pk: { S: PROJECT }, sk: { S: targetsSortKey } },
    },
  ]);
  expect(cloudfront.calls.map((call) => call.DistributionId).sort()).toEqual([
    "E1PROD",
    "E2SECOND",
  ]);
  expect(cloudfront.calls[0].InvalidationBatch.Paths).toEqual({
    Quantity: 1,
    Items: [`#${RELEASE}|products`],
  });
});

it("reaches the bootstrap's wildcard as well as the project's own front", async () => {
  const both = new FakeDynamo(
    new Map([
      [bootstrapPartition(CLASS), ["EWILDCARD"]],
      [PROJECT, ["E1PROD"]],
    ]),
  );

  await invalidateAll(invalidator(both, cloudfront), raises(["products"]));

  expect(cloudfront.calls.map((call) => call.DistributionId).sort()).toEqual([
    "E1PROD",
    "EWILDCARD",
  ]);
});

it("reaches the wildcard for a project that names no front of its own", async () => {
  const wildcardOnly = new FakeDynamo(new Map([[bootstrapPartition(CLASS), ["EWILDCARD"]]]));

  await invalidateAll(invalidator(wildcardOnly, cloudfront), raises(["products"]));

  expect(cloudfront.calls.map((call) => call.DistributionId)).toEqual(["EWILDCARD"]);
});

it("reads the targets once for a batch that raises the same project twice", async () => {
  const both: Raises = new Map([
    [PREFIX, { tags: ["products"], sequenceNumbers: ["seq-1"] }],
    ["prod/acme/api/r1111111a/isr", { tags: ["cart"], sequenceNumbers: ["seq-2"] }],
  ]);

  await invalidateAll(invalidator(dynamo, cloudfront), both);

  expect(dynamo.reads).toHaveLength(2);
  expect(cloudfront.calls).toHaveLength(4);
});

it("sends soft tags first, in batches of at most one request's worth", async () => {
  const soft = Array.from({ length: pathsPerInvalidation }, (_, i) => `_N_T_/s${i}`);
  const dynamoOne = new FakeDynamo(new Map([[PROJECT, ["E1PROD"]]]));

  await invalidateAll(invalidator(dynamoOne, cloudfront), raises(["products", ...soft]));

  const items = cloudfront.calls.map((call) => call.InvalidationBatch.Paths.Items);
  expect(items).toEqual([soft.map((tag) => `#${RELEASE}|${tag}`), [`#${RELEASE}|products`]]);
  const references = cloudfront.calls.map((call) => call.InvalidationBatch.CallerReference);
  expect(new Set(references).size).toBe(references.length);
});

it("names a batch by what it carries, so a redrive re-sends nothing new", async () => {
  const first = new FakeCloudFront();
  const second = new FakeCloudFront();

  await invalidateAll(invalidator(dynamo, first), raises(["products"]));
  await invalidateAll(invalidator(dynamo, second), raises(["products"]));

  expect(second.calls.map((call) => call.InvalidationBatch.CallerReference)).toEqual(
    first.calls.map((call) => call.InvalidationBatch.CallerReference),
  );
});

it("names a later raise of the same tag differently, so it is not swallowed", async () => {
  const later: Raises = new Map([[PREFIX, { tags: ["products"], sequenceNumbers: ["seq-2"] }]]);

  await invalidateAll(invalidator(dynamo, cloudfront), raises(["products"]));
  const before = cloudfront.calls[0].InvalidationBatch.CallerReference;
  await invalidateAll(invalidator(dynamo, cloudfront), later);
  const after = cloudfront.calls.at(-1)!.InvalidationBatch.CallerReference;

  expect(after).not.toBe(before);
});

it("treats a batch CloudFront already holds as sent", async () => {
  cloudfront.fail = named("InvalidationBatchAlreadyExists");

  const failed = await invalidateAll(invalidator(dynamo, cloudfront), raises(["products"]));

  expect(failed).toEqual([]);
});

it("keeps reaching the live fronts when one of them no longer exists", async () => {
  vi.spyOn(console, "warn").mockImplementation(() => {});
  cloudfront.refuse.set("E1PROD", named("NoSuchDistribution"));

  const failed = await invalidateAll(invalidator(dynamo, cloudfront), raises(["products"]));

  expect(failed).toEqual([]);
  expect(cloudfront.calls.some((call) => call.DistributionId === "E2SECOND")).toBe(true);
});

it("stops reaching a front that is gone rather than spending every later batch on it", async () => {
  const many = Array.from({ length: pathsPerInvalidation + 1 }, (_, i) => `t${i}`);
  vi.spyOn(console, "warn").mockImplementation(() => {});
  cloudfront.refuse.set("E1PROD", named("NoSuchDistribution"));

  await invalidateAll(invalidator(dynamo, cloudfront), raises(many));

  expect(cloudfront.calls.filter((call) => call.DistributionId === "E1PROD")).toHaveLength(1);
  expect(cloudfront.calls.filter((call) => call.DistributionId === "E2SECOND")).toHaveLength(2);
});

it("waits out a congested distribution rather than failing the batch", async () => {
  const waits: number[] = [];
  const congested = new FakeCloudFront();
  let refusals = 2;
  congested.send = async (command: any) => {
    congested.calls.push(command.input);
    if (refusals-- > 0) throw named("TooManyInvalidationsInProgress");
    return {};
  };

  const failed = await invalidateAll(
    {
      ...invalidator(dynamo, congested),
      sleep: async (ms: number) => {
        waits.push(ms);
      },
    },
    raises(["products"]),
  );

  expect(failed).toEqual([]);
  expect(waits).toHaveLength(2);
});

it("gives up on a distribution that stays congested and fails its records", async () => {
  vi.spyOn(console, "error").mockImplementation(() => {});
  cloudfront.fail = named("TooManyInvalidationsInProgress");

  const failed = await invalidateAll(invalidator(dynamo, cloudfront), raises(["products"]));

  expect(failed).toEqual(["seq-1"]);
});

it("invalidates nothing when the ledger names no target", async () => {
  vi.spyOn(console, "warn").mockImplementation(() => {});
  const empty = new FakeDynamo();

  const failed = await invalidateAll(invalidator(empty, cloudfront), raises(["products"]));

  expect(failed).toEqual([]);
  expect(cloudfront.calls).toEqual([]);
});

it("says so when the ledger names no target", async () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

  await invalidateAll(invalidator(new FakeDynamo(), cloudfront), raises(["products"]));

  expect(warn.mock.calls.flat().join(" ")).toContain("acme");
});

it("says so when a tag CloudFront never stored is raised", async () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

  await invalidateAll(invalidator(dynamo, cloudfront), raises(["with space"]));

  expect(warn.mock.calls.flat().join(" ")).toContain("with space");
});

it("fails the records of a build whose targets cannot be read", async () => {
  vi.spyOn(console, "error").mockImplementation(() => {});
  dynamo.fail = new Error("ProvisionedThroughputExceededException");

  const failed = await invalidateAll(invalidator(dynamo, cloudfront), raises(["products"]));

  expect(failed).toEqual(["seq-1"]);
});

it("fails the records of a build CloudFront refused", async () => {
  vi.spyOn(console, "error").mockImplementation(() => {});
  cloudfront.fail = named("AccessDenied");

  const failed = await invalidateAll(invalidator(dynamo, cloudfront), raises(["products"]));

  expect(failed).toEqual(["seq-1"]);
});

it("says so, and invents no target, for a prefix it cannot read", async () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

  const failed = await invalidateAll(
    invalidator(dynamo, cloudfront),
    raises(["products"], "nonsense"),
  );

  expect(failed).toEqual([]);
  expect(dynamo.reads).toEqual([]);
  expect(cloudfront.calls).toEqual([]);
  expect(warn.mock.calls.flat().join(" ")).toContain("nonsense");
});

import { createHmac } from "node:crypto";
import { derivePurgeSecret, purgeSignatureHeader } from "@platform/edge-contract/purge";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { invalidateAll } from "../src/invalidate.mjs";
import type { Raises } from "../src/records.mjs";
import { pathsPerInvalidation } from "../src/tags.mjs";

const TABLE = "ocel-state";
const TIER = "production";
const PREFIX = "prod/acme/web/r0a1b2c3d/isr";
const RELEASE = "r0a1b2c3d";
const PROJECT = "routers#cloudfront#acme";
const WILDCARD = "routers#cloudfront";

class FakeDynamo {
  reads: any[] = [];
  fail: Error | null = null;
  constructor(public items = new Map<string, string[]>()) {}

  async send(command: any): Promise<any> {
    this.reads.push(command.input);
    if (this.fail !== null) throw this.fail;
    const distributions = this.items.get(command.input.Key.pk.S);
    return distributions === undefined
      ? {}
      : { Item: { value: { S: JSON.stringify(distributions) } } };
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

function raiseOf(tags: string[], sequenceNumbers: string[], raisedAt = 1_000) {
  return {
    tags,
    entries: tags.map((tag) => ({ tag, mode: "purge" as const, raisedAt })),
    sequenceNumbers,
  };
}

function raises(tags: string[], prefix = PREFIX): Raises {
  return new Map([[prefix, raiseOf(tags, ["seq-1"])]]);
}

function invalidator(dynamo: FakeDynamo, cloudfront: FakeCloudFront) {
  return {
    cloudfront,
    dynamo,
    commands,
    table: TABLE,
    bootstrapTier: TIER,
    sleep: async () => {},
  };
}

let dynamo: FakeDynamo;
let cloudfront: FakeCloudFront;

beforeEach(() => {
  dynamo = new FakeDynamo(new Map([[PROJECT, ["E1PROD", "E2SECOND"]]]));
  cloudfront = new FakeCloudFront();
});

it("invalidates every distribution the CloudFront router names for the project", async () => {
  const failed = await invalidateAll(invalidator(dynamo, cloudfront), raises(["products"]));

  expect(failed).toEqual([]);
  expect(dynamo.reads).toEqual([
    {
      TableName: TABLE,
      ConsistentRead: true,
      Key: { pk: { S: WILDCARD }, sk: { S: "invalidation#" } },
    },
    {
      TableName: TABLE,
      ConsistentRead: true,
      Key: { pk: { S: PROJECT }, sk: { S: "invalidation#" } },
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
      [WILDCARD, ["EWILDCARD"]],
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
  const wildcardOnly = new FakeDynamo(new Map([[WILDCARD, ["EWILDCARD"]]]));

  await invalidateAll(invalidator(wildcardOnly, cloudfront), raises(["products"]));

  expect(cloudfront.calls.map((call) => call.DistributionId)).toEqual(["EWILDCARD"]);
});

it("reads the targets once for a batch that raises the same project twice", async () => {
  const both: Raises = new Map([
    [PREFIX, raiseOf(["products"], ["seq-1"])],
    ["prod/acme/api/r1111111a/isr", raiseOf(["cart"], ["seq-2"])],
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

it("names a batch by what it contains, so a redrive re-sends nothing new", async () => {
  const first = new FakeCloudFront();
  const second = new FakeCloudFront();

  await invalidateAll(invalidator(dynamo, first), raises(["products"]));
  await invalidateAll(invalidator(dynamo, second), raises(["products"]));

  expect(second.calls.map((call) => call.InvalidationBatch.CallerReference)).toEqual(
    first.calls.map((call) => call.InvalidationBatch.CallerReference),
  );
});

it("names a later raise of the same tag differently, so it is not swallowed", async () => {
  const later: Raises = new Map([[PREFIX, raiseOf(["products"], ["seq-2"])]]);

  await invalidateAll(invalidator(dynamo, cloudfront), raises(["products"]));
  const before = cloudfront.calls[0].InvalidationBatch.CallerReference;
  await invalidateAll(invalidator(dynamo, cloudfront), later);
  const after = cloudfront.calls.at(-1)!.InvalidationBatch.CallerReference;

  expect(after).not.toBe(before);
});

it("treats a batch CloudFront already has as sent", async () => {
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

it("invalidates nothing when the CloudFront router names no target", async () => {
  vi.spyOn(console, "warn").mockImplementation(() => {});
  const empty = new FakeDynamo();

  const failed = await invalidateAll(invalidator(empty, cloudfront), raises(["products"]));

  expect(failed).toEqual([]);
  expect(cloudfront.calls).toEqual([]);
});

it("says so when the CloudFront router names no target", async () => {
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

describe("the Cloudflare target", () => {
  const CLOUDFLARE_PROJECT = "routers#cloudflare#acme";
  const PURGE_URL = "https://acme-prod.sub.workers.dev/.ocel/purge";
  const SEED = "seed-1";

  class FakeWorkers {
    sent: { url: string; body: any; signature: string | null }[] = [];
    refuse: Response | Error | null = null;
    gone = new Set<string>();

    fetch = (async (url: string, init: RequestInit) => {
      this.sent.push({
        url,
        body: JSON.parse(String(init.body)),
        signature: new Headers(init.headers).get(purgeSignatureHeader),
      });
      if (this.gone.has(url)) return new Response("Not Found", { status: 404 });
      if (this.refuse instanceof Error) throw this.refuse;
      return this.refuse?.clone() ?? new Response(null, { status: 202 });
    }) as unknown as typeof fetch;
  }

  let workers: FakeWorkers;
  let seedReads: number;

  function cloudflareInvalidator(over: Partial<ReturnType<typeof invalidator>> = {}) {
    return {
      ...invalidator(dynamo, cloudfront),
      ...over,
      cloudflare: {
        fetch: workers.fetch,
        readSeed: async () => {
          seedReads++;
          return SEED;
        },
        sleep: async () => {},
        random: () => 1,
      },
    };
  }

  beforeEach(() => {
    workers = new FakeWorkers();
    seedReads = 0;
    dynamo = new FakeDynamo(new Map([[CLOUDFLARE_PROJECT, [PURGE_URL]]]));
  });

  it("posts the raise, signed with the release's secret, to the URL the project's edge named", async () => {
    const failed = await invalidateAll(
      cloudflareInvalidator(),
      new Map([
        [
          PREFIX,
          {
            ...raiseOf([], ["seq-1"]),
            entries: [{ tag: "_N_T_/blog/a", mode: "purge", raisedAt: 5_000 }],
          },
        ],
      ]),
    );

    expect(failed).toEqual([]);
    expect(workers.sent).toHaveLength(1);
    const sent = workers.sent[0]!;
    expect(sent.url).toBe(PURGE_URL);
    expect(sent.body).toEqual({
      isrPrefix: PREFIX,
      mode: "purge",
      tags: [{ tag: "_N_T_/blog/a", raisedAt: 5_000 }],
    });
    const secret = derivePurgeSecret(SEED, PREFIX);
    expect(sent.signature).toBe(
      createHmac("sha256", secret).update(JSON.stringify(sent.body)).digest("hex"),
    );
  });

  it("reads the project's own Cloudflare partition and no CloudFront front", async () => {
    await invalidateAll(cloudflareInvalidator(), raises(["products"]));

    expect(dynamo.reads.map((read) => read.Key.pk.S)).toEqual([
      WILDCARD,
      PROJECT,
      CLOUDFLARE_PROJECT,
    ]);
    expect(cloudfront.calls).toEqual([]);
  });

  it("reaches every URL the project names, and reads the seed once for a batch", async () => {
    dynamo = new FakeDynamo(
      new Map([
        [CLOUDFLARE_PROJECT, [PURGE_URL, "https://acme-staging.sub.workers.dev/.ocel/purge"]],
      ]),
    );
    const both: Raises = new Map([
      [PREFIX, raiseOf(["a"], ["seq-1"])],
      ["prod/acme/api/r1111111a/isr", raiseOf(["b"], ["seq-2"])],
    ]);

    await invalidateAll(cloudflareInvalidator(), both);

    expect(workers.sent.map((sent) => `${sent.body.isrPrefix} ${sent.url}`).sort()).toEqual([
      `prod/acme/api/r1111111a/isr ${PURGE_URL}`,
      "prod/acme/api/r1111111a/isr https://acme-staging.sub.workers.dev/.ocel/purge",
      `${PREFIX} ${PURGE_URL}`,
      `${PREFIX} https://acme-staging.sub.workers.dev/.ocel/purge`,
    ]);
    expect(seedReads).toBe(1);
  });

  it("reaches CloudFront and Cloudflare for a project that names both", async () => {
    dynamo = new FakeDynamo(
      new Map([
        [PROJECT, ["E1PROD"]],
        [CLOUDFLARE_PROJECT, [PURGE_URL]],
      ]),
    );

    const failed = await invalidateAll(cloudflareInvalidator(), raises(["products"]));

    expect(failed).toEqual([]);
    expect(cloudfront.calls.map((call) => call.DistributionId)).toEqual(["E1PROD"]);
    expect(workers.sent).toHaveLength(1);
  });

  it("does not read the seed for a project that names no Cloudflare target", async () => {
    dynamo = new FakeDynamo(new Map([[PROJECT, ["E1PROD"]]]));

    await invalidateAll(cloudflareInvalidator(), raises(["products"]));

    expect(seedReads).toBe(0);
    expect(workers.sent).toEqual([]);
  });

  it("works for an account with no Cloudflare support at all", async () => {
    dynamo = new FakeDynamo(new Map([[PROJECT, ["E1PROD"]]]));

    const failed = await invalidateAll(invalidator(dynamo, cloudfront), raises(["products"]));

    expect(failed).toEqual([]);
    expect(cloudfront.calls).toHaveLength(1);
  });

  it("fails the records of a build whose purge Cloudflare refused", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    workers.refuse = new Response(null, { status: 401 });

    const failed = await invalidateAll(cloudflareInvalidator(), raises(["products"]));

    expect(failed).toEqual(["seq-1"]);
  });

  it("skips a URL whose worker is gone, warning, and purges at the URLs still there", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const STAGING = "https://acme-staging.sub.workers.dev/.ocel/purge";
    dynamo = new FakeDynamo(new Map([[CLOUDFLARE_PROJECT, [PURGE_URL, STAGING]]]));
    workers.gone.add(STAGING);

    const failed = await invalidateAll(cloudflareInvalidator(), raises(["products"]));

    expect(failed).toEqual([]);
    expect(workers.sent.map((sent) => sent.url).sort()).toEqual([PURGE_URL, STAGING]);
    expect(warn.mock.calls.flat().join(" ")).toContain(STAGING);
  });

  it("fails the records of a build whose seed cannot be read", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});

    const failed = await invalidateAll(
      {
        ...cloudflareInvalidator(),
        cloudflare: {
          fetch: workers.fetch,
          readSeed: async () => {
            throw new Error("ParameterNotFound");
          },
        },
      },
      raises(["products"]),
    );

    expect(failed).toEqual(["seq-1"]);
    expect(workers.sent).toEqual([]);
  });

  it("does not warn that no front is named when only Cloudflare is", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

    await invalidateAll(cloudflareInvalidator(), raises(["products"]));

    expect(warn).not.toHaveBeenCalled();
  });
});

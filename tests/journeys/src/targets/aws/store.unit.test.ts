import assert from "node:assert/strict";
import { describe, it } from "bun:test";
import { awsLinkStore, awsStore, type Cli } from "./store";

const TABLES: Record<string, string> = {
  StateTableName: "ocel-state",
  VarsTableName: "ocel-vars",
};

const STATE_TABLE = TABLES.StateTableName as string;

function page(slugs: string[], next?: string): string {
  return JSON.stringify({
    Items: slugs.map((slug) => ({ sk: { S: `${slug}#` } })),
    ...(next ? { NextToken: next } : {}),
  });
}

function cliOver(answers: (args: string[]) => string | Promise<string>): {
  cli: Cli;
  calls: string[][];
} {
  const calls: string[][] = [];
  return {
    calls,
    cli: async (args) => {
      calls.push(args);
      return answers(args);
    },
  };
}

function describeStacks(args: string[]): boolean {
  return args[0] === "cloudformation";
}

function outputAsked(args: string[]): string {
  return /OutputKey=='([^']+)'/.exec(args.join(" "))?.[1] ?? "";
}

function tableAsked(args: string[]): string {
  return TABLES[outputAsked(args)] ?? "None";
}

function outputsAsked(calls: string[][]): string[] {
  return [...new Set(calls.filter(describeStacks).map(outputAsked))];
}

describe("deployedSlugs", () => {
  it("refuses to report an empty account when the bootstrap stack could not be read", async () => {
    const { cli } = cliOver(() => {
      throw Object.assign(new Error("Command failed"), {
        stderr: "Unable to locate credentials",
      });
    });
    await assert.rejects(
      awsStore(undefined, cli).deployedSlugs(),
      /could not be read.*Unable to locate credentials/s,
    );
  });

  it("refuses when the stack stands but publishes no table name", async () => {
    const { cli } = cliOver(() => "None");
    await assert.rejects(awsStore(undefined, cli).deployedSlugs(), /publishes no StateTableName/);
  });

  it("reports nothing when no bootstrap stack exists", async () => {
    const { cli } = cliOver(() => {
      throw Object.assign(new Error("Command failed"), {
        stderr: "ValidationError: Stack with id ocel-bootstrap does not exist",
      });
    });
    assert.deepEqual(await awsStore(undefined, cli).deployedSlugs(), []);
  });

  it("asks for the state table the projects and edge stacks are kept in", async () => {
    const { cli, calls } = cliOver((args) => (describeStacks(args) ? tableAsked(args) : page([])));
    await awsStore(undefined, cli).deployedSlugs();
    assert.deepEqual(outputsAsked(calls), ["StateTableName"]);
    for (const queried of calls.filter((args) => args[0] === "dynamodb")) {
      assert.ok(queried.includes(STATE_TABLE));
    }
  });

  it("pages both production partitions", async () => {
    const { cli, calls } = cliOver((args) => {
      if (describeStacks(args)) {
        return tableAsked(args);
      }
      return args.includes("--starting-token")
        ? page(["j-1-hello"])
        : page(["j-1-node"], "more");
    });
    assert.deepEqual(await awsStore(undefined, cli).deployedSlugs(), ["j-1-node", "j-1-hello"]);
    assert.equal(calls.filter((args) => args[0] === "dynamodb").length, 4);
  });
});

describe("stands", () => {
  it("reaches the slug with a key condition rather than paging the partition", async () => {
    const { cli, calls } = cliOver((args) =>
      describeStacks(args) ? tableAsked(args) : page(["j-1-node"]),
    );
    assert.equal(await awsStore(undefined, cli).stands("j-1-node"), true);
    const queried = calls.filter((args) => args[0] === "dynamodb");
    assert.equal(queried.length, 1);
    assert.ok(queried[0]?.includes("pk = :pk AND begins_with(sk, :sk)"));
    assert.ok(
      queried[0]?.includes(
        JSON.stringify({ ":pk": { S: "projects#production" }, ":sk": { S: "j-1-node" } }),
      ),
    );
  });

  it("does not mistake a longer slug sharing the prefix for the one asked about", async () => {
    const { cli } = cliOver((args) =>
      describeStacks(args) ? tableAsked(args) : page(["j-1-node-two"]),
    );
    assert.equal(await awsStore(undefined, cli).stands("j-1-node"), false);
  });

  it("carries the failure out rather than answering that the slug is gone", async () => {
    const { cli } = cliOver((args) => {
      if (describeStacks(args)) {
        throw Object.assign(new Error("Command failed"), { stderr: "ThrottlingException" });
      }
      return tableAsked(args);
    });
    await assert.rejects(awsStore(undefined, cli).stands("j-1-node"), /ThrottlingException/);
  });
});

const SLUG = "j-1-with-sst";

function b64(value: string): string {
  return Buffer.from(value, "utf8").toString("base64");
}

function linksPage(items: Array<{ sk: string; body: Record<string, unknown> }>): string {
  return JSON.stringify({
    Items: items.map(({ sk, body }) => ({
      sk: { S: sk },
      body: { B: b64(JSON.stringify(body)) },
    })),
  });
}

function recordItem(sk: string, record: Record<string, unknown>, owner: string): { sk: string; body: Record<string, unknown> } {
  return {
    sk,
    body: { version: 1, updatedAt: 0, record: b64(JSON.stringify(record)), owner },
  };
}

describe("awsLinkStore", () => {
  const owner = "urn:pulumi:j-1::with-sst::pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource::ocel-link-orders";

  it("asks for the state table the provider writes link values into", async () => {
    const { cli, calls } = cliOver((args) =>
      describeStacks(args) ? tableAsked(args) : linksPage([]),
    );
    await awsLinkStore(undefined, cli).records(SLUG);
    assert.deepEqual(outputsAsked(calls), ["StateTableName"]);
    assert.ok(calls.find((args) => args[0] === "dynamodb")?.includes(STATE_TABLE));
  });

  it("refuses when the stack publishes no state table name", async () => {
    const { cli } = cliOver(() => "None");
    await assert.rejects(
      awsLinkStore(undefined, cli).records(SLUG),
      /publishes no StateTableName output, so no link can be read/,
    );
  });

  it("parses a postgres record and a custom record, redacted and stamped with the owner", async () => {
    const { cli } = cliOver((args) => {
      if (describeStacks(args)) {
        return tableAsked(args);
      }
      return linksPage([
        recordItem(
          "links#orders#records#*#",
          {
            name: "orders",
            postgres: {},
            source: "sst",
            grants: [{ actions: ["rds-db:connect"], resources: ["arn:aws:rds-db:x"], label: "connect" }],
          },
          owner,
        ),
        recordItem("links#network#records#*#", { name: "network", custom: {}, source: "sst" }, owner),
      ]);
    });

    const records = await awsLinkStore(undefined, cli).records(SLUG);
    assert.equal(records.length, 2);
    const orders = records.find((row) => row.name === "orders");
    assert.deepEqual(orders, {
      name: "orders",
      type: "postgres",
      source: "sst",
      owner,
      grants: [{ actions: ["rds-db:connect"], resources: ["arn:aws:rds-db:x"], label: "connect" }],
      redactedProperties: {},
    });
    const network = records.find((row) => row.name === "network");
    assert.deepEqual(network, {
      name: "network",
      type: "custom",
      source: "sst",
      owner,
      grants: [],
      redactedProperties: {},
    });
  });

  it("parses a value item as the sealed ciphertext beside nothing else", async () => {
    const sealed = b64("kms-ciphertext-not-plaintext");
    const { cli } = cliOver((args) =>
      describeStacks(args)
        ? tableAsked(args)
        : linksPage([{ sk: "links#orders#values#*#", body: { version: 1, sealed } }]),
    );
    const values = await awsLinkStore(undefined, cli).values(SLUG);
    assert.deepEqual(values, [{ name: "orders", sealed }]);
  });

  it("reads the publisher's owner index by its own prefix", async () => {
    const { cli, calls } = cliOver((args) =>
      describeStacks(args)
        ? tableAsked(args)
        : linksPage([{ sk: `linkowners#${owner}#*#`, body: { names: ["orders"] } }]),
    );
    const names = await awsLinkStore(undefined, cli).ownerIndex(SLUG, owner);
    assert.deepEqual(names, ["orders"]);
    const queried = calls.find((args) => args[0] === "dynamodb");
    assert.ok(queried?.includes(JSON.stringify({ ":pk": { S: `values#${SLUG}#production` }, ":sk": { S: `linkowners#${owner}#` } })));
  });

  it("reports no index for an owner that never published there", async () => {
    const { cli } = cliOver((args) => (describeStacks(args) ? tableAsked(args) : linksPage([])));
    assert.equal(await awsLinkStore(undefined, cli).ownerIndex(SLUG, owner), undefined);
  });
});

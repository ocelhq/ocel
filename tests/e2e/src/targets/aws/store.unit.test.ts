import { describe, it } from "bun:test";
import assert from "node:assert/strict";
import {
  awsBindingStore,
  awsStore,
  type Cli,
  partitionKey,
  sortKey,
  taggedNamespaces,
} from "./store";

const TABLES: Record<string, string> = {
  VariablesTableName: "ocel-variables",
  StateBucketName: "ocel-statebucket",
};

const VARIABLES_TABLE = TABLES.VariablesTableName as string;

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

describe("callerAccount", () => {
  it("names the account the credentials resolve to", async () => {
    const identity: Record<string, string> = {
      UserId: "AROAEXAMPLE:journey",
      Account: "111122223333",
      Arn: "arn:aws:sts::111122223333:assumed-role/journey/journey",
    };
    const { cli } = cliOver((args) => {
      assert.deepEqual(args.slice(0, 2), ["sts", "get-caller-identity"]);
      const field = args[args.indexOf("--query") + 1] ?? "";
      return identity[field] ?? "None";
    });
    assert.equal(await awsStore(undefined, cli).callerAccount(), "111122223333");
  });
});

describe("hasBootstrap", () => {
  it("answers that the namespace is bootstrapped when its bootstrap stack exists", async () => {
    const { cli } = cliOver(() => "CREATE_COMPLETE");
    assert.equal(await awsStore(undefined, cli).hasBootstrap(), true);
  });

  it("answers that the namespace is not bootstrapped when no bootstrap stack exists", async () => {
    const { cli } = cliOver(() => {
      throw Object.assign(new Error("Command failed"), {
        stderr: "ValidationError: Stack with id ocel-prod-bootstrap does not exist",
      });
    });
    assert.equal(await awsStore(undefined, cli).hasBootstrap(), false);
  });

  it("refuses to answer when the bootstrap stack could not be read", async () => {
    const { cli } = cliOver(() => {
      throw Object.assign(new Error("Command failed"), {
        stderr: "Unable to locate credentials",
      });
    });
    await assert.rejects(
      awsStore(undefined, cli).hasBootstrap(),
      /could not be read.*Unable to locate credentials/s,
    );
  });
});

describe("releaseLocks", () => {
  it("removes only the locks under the slug's own backend in the namespace's state bucket", async () => {
    const { cli, calls } = cliOver((args) => (describeStacks(args) ? tableAsked(args) : ""));
    await awsStore(undefined, cli, "j-9-next").releaseLocks("j-9-deploy-next");
    assert.ok(
      calls.filter(describeStacks).every((args) => args.includes("j-9-next-prod-bootstrap")),
    );
    assert.deepEqual(
      calls.filter((args) => args[0] === "s3"),
      [["s3", "rm", "s3://ocel-statebucket/j-9-deploy-next/.pulumi/locks/", "--recursive"]],
    );
  });

  it("touches nothing when the namespace has no bootstrap", async () => {
    const { cli, calls } = cliOver((args) => {
      if (describeStacks(args)) {
        throw Object.assign(new Error("Command failed"), { stderr: "Stack does not exist" });
      }
      return "";
    });
    await awsStore(undefined, cli).releaseLocks("j-1-node");
    assert.equal(calls.filter((args) => args[0] === "s3").length, 0);
  });

  it("refuses when the bootstrap publishes no state bucket", async () => {
    const { cli } = cliOver(() => "None");
    await assert.rejects(awsStore(undefined, cli).releaseLocks("j-1-node"), /StateBucketName/);
  });
});

const SLUG = "j-1-with-sst";

function b64(value: string): string {
  return Buffer.from(value, "utf8").toString("base64");
}

function bindingsPage(items: Array<{ sk: string; body: Record<string, unknown> }>): string {
  return JSON.stringify({
    Items: items.map(({ sk, body }) => ({
      sk: { S: sk },
      value: { S: JSON.stringify(body) },
    })),
  });
}

function recordItem(
  sk: string,
  record: Record<string, unknown>,
  owner: string,
): { sk: string; body: Record<string, unknown> } {
  return {
    sk,
    body: { version: 1, updatedAt: 0, record: b64(JSON.stringify(record)), owner },
  };
}

describe("awsBindingStore", () => {
  const owner =
    "urn:pulumi:j-1::with-sst::pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource::ocel-binding-orders";

  it("asks for the variables table the provider writes binding values into", async () => {
    const { cli, calls } = cliOver((args) =>
      describeStacks(args) ? tableAsked(args) : bindingsPage([]),
    );
    await awsBindingStore(undefined, cli).records(SLUG);
    assert.deepEqual(outputsAsked(calls), ["VariablesTableName"]);
    const queried = calls.find((args) => args[0] === "dynamodb");
    assert.ok(queried?.includes(VARIABLES_TABLE));
    assert.ok(
      queried?.includes(
        JSON.stringify({ ":pk": { S: `values#${SLUG}` }, ":sk": { S: "bindings#" } }),
      ),
    );
  });

  it("refuses when the stack publishes no variables table name", async () => {
    const { cli } = cliOver(() => "None");
    await assert.rejects(
      awsBindingStore(undefined, cli).records(SLUG),
      /publishes no VariablesTableName output, so no binding can be read/,
    );
  });

  it("refuses an item with no JSON value rather than read it as no binding", async () => {
    const { cli } = cliOver((args) =>
      describeStacks(args)
        ? tableAsked(args)
        : JSON.stringify({
            Items: [{ sk: { S: "bindings#orders#values#*#" }, body: { B: "e30=" } }],
          }),
    );
    await assert.rejects(awsBindingStore(undefined, cli).values(SLUG), /no JSON value/);
  });

  it("parses a postgres record and a custom record, redacted and stamped with the owner", async () => {
    const { cli } = cliOver((args) => {
      if (describeStacks(args)) {
        return tableAsked(args);
      }
      return bindingsPage([
        recordItem(
          "bindings#orders#records#*#",
          {
            binding: { name: "orders", postgres: {}, source: "sst" },
            grants: [
              { actions: ["rds-db:connect"], resources: ["arn:aws:rds-db:x"], label: "connect" },
            ],
          },
          owner,
        ),
        recordItem(
          "bindings#network#records#*#",
          { binding: { name: "network", custom: {}, source: "sst" } },
          owner,
        ),
      ]);
    });

    const records = await awsBindingStore(undefined, cli).records(SLUG);
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
        : bindingsPage([{ sk: "bindings#orders#values#*#", body: { version: 1, sealed } }]),
    );
    const values = await awsBindingStore(undefined, cli).values(SLUG);
    assert.deepEqual(values, [{ name: "orders", sealed }]);
  });

  it("reads the publisher's owner index by its own prefix", async () => {
    const { cli, calls } = cliOver((args) =>
      describeStacks(args)
        ? tableAsked(args)
        : bindingsPage([{ sk: `bindingowners#${owner}#*#`, body: { names: ["orders"] } }]),
    );
    const names = await awsBindingStore(undefined, cli).ownerIndex(SLUG, owner);
    assert.deepEqual(names, ["orders"]);
    const queried = calls.find((args) => args[0] === "dynamodb");
    assert.ok(
      queried?.includes(
        JSON.stringify({
          ":pk": { S: `values#${SLUG}` },
          ":sk": { S: `bindingowners#${owner}#` },
        }),
      ),
    );
  });

  it("reports no index for an owner that never published there", async () => {
    const { cli } = cliOver((args) => (describeStacks(args) ? tableAsked(args) : bindingsPage([])));
    assert.equal(await awsBindingStore(undefined, cli).ownerIndex(SLUG, owner), undefined);
  });
});

describe("the keys the store is read by", () => {
  it("joins a partition's root and path with # as the Go store does", () => {
    assert.equal(partitionKey("projects"), "projects");
    assert.equal(partitionKey("values", "j-1-node"), "values#j-1-node");
  });

  it("ends a sort key with the separator, so no key prefixes a longer one", () => {
    assert.equal(sortKey(["bindings", "orders", "records", "*"]), "bindings#orders#records#*#");
  });

  it("escapes a segment's # and % byte by byte, and nothing else", () => {
    assert.equal(partitionKey("values", "a#b%c/d"), "values#a%23b%25c/d");
    assert.equal(sortKey(["café#"]), "café%23#");
  });
});

describe("the namespace a store reads under", () => {
  it("reads the default bootstrap stack when no namespace is named", async () => {
    const { cli, calls } = cliOver(() => "CREATE_COMPLETE");
    await awsStore(undefined, cli).hasBootstrap();
    const named = calls.find(describeStacks)?.includes("ocel-prod-bootstrap");
    assert.ok(named, "the store read a stack other than ocel-prod-bootstrap");
  });

  it("reads the bootstrap of the namespace it was given", async () => {
    const { cli, calls } = cliOver(() => "CREATE_COMPLETE");
    await awsStore(undefined, cli, "j-1874-node").hasBootstrap();
    const named = calls.find(describeStacks)?.includes("j-1874-node-prod-bootstrap");
    assert.ok(named, "the store did not read the namespaced bootstrap stack");
  });
});

describe("taggedNamespaces", () => {
  function tagged(namespaces: string[], next?: string): string {
    return JSON.stringify({
      ResourceTagMappingList: namespaces.map((namespace) => ({
        Tags: [{ Key: "ocel:namespace", Value: namespace }],
      })),
      ...(next ? { PaginationToken: next } : {}),
    });
  }

  it("asks the tag index for bootstrap stacks alone, never every stack in the account", async () => {
    const { cli, calls } = cliOver(() => tagged([]));
    await taggedNamespaces(cli);
    const asked = calls[0] ?? [];
    assert.equal(asked[0], "resourcegroupstaggingapi");
    assert.ok(asked.includes("cloudformation:stack"), asked.join(" "));
    assert.ok(asked.includes("Key=ocel:namespace"), asked.join(" "));
  });

  it("reads every namespace the account has tagged, across pages", async () => {
    const pages = [tagged(["j-1799-one"], "more"), tagged(["j-1799-two", "ocel"])];
    const { cli } = cliOver(() => pages.shift() ?? tagged([]));
    assert.deepEqual(await taggedNamespaces(cli), ["j-1799-one", "j-1799-two", "ocel"]);
  });
});

import assert from "node:assert/strict";
import { describe, it } from "vitest";
import { awsStore, type Cli } from "./store";

const TABLE = "ocel-vars";

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
    await assert.rejects(awsStore(undefined, cli).deployedSlugs(), /publishes no VarsTableName/);
  });

  it("reports nothing when no bootstrap stack exists", async () => {
    const { cli } = cliOver(() => {
      throw Object.assign(new Error("Command failed"), {
        stderr: "ValidationError: Stack with id ocel-bootstrap does not exist",
      });
    });
    assert.deepEqual(await awsStore(undefined, cli).deployedSlugs(), []);
  });

  it("pages both production partitions", async () => {
    const { cli, calls } = cliOver((args) => {
      if (describeStacks(args)) {
        return TABLE;
      }
      return args.includes("--starting-token")
        ? page(["j-1-hello"])
        : page(["j-1-express"], "more");
    });
    assert.deepEqual(await awsStore(undefined, cli).deployedSlugs(), ["j-1-express", "j-1-hello"]);
    assert.equal(calls.filter((args) => args[0] === "dynamodb").length, 4);
  });
});

describe("stands", () => {
  it("reaches the slug with a key condition rather than paging the partition", async () => {
    const { cli, calls } = cliOver((args) => (describeStacks(args) ? TABLE : page(["j-1-express"])));
    assert.equal(await awsStore(undefined, cli).stands("j-1-express"), true);
    const queried = calls.filter((args) => args[0] === "dynamodb");
    assert.equal(queried.length, 1);
    assert.ok(queried[0]?.includes("pk = :pk AND begins_with(sk, :sk)"));
    assert.ok(
      queried[0]?.includes(
        JSON.stringify({ ":pk": { S: "projects#production" }, ":sk": { S: "j-1-express" } }),
      ),
    );
  });

  it("does not mistake a longer slug sharing the prefix for the one asked about", async () => {
    const { cli } = cliOver((args) =>
      describeStacks(args) ? TABLE : page(["j-1-express-two"]),
    );
    assert.equal(await awsStore(undefined, cli).stands("j-1-express"), false);
  });

  it("carries the failure out rather than answering that the slug is gone", async () => {
    const { cli } = cliOver((args) => {
      if (describeStacks(args)) {
        throw Object.assign(new Error("Command failed"), { stderr: "ThrottlingException" });
      }
      return TABLE;
    });
    await assert.rejects(awsStore(undefined, cli).stands("j-1-express"), /ThrottlingException/);
  });
});

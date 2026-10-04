import { describe, it } from "bun:test";
import assert from "node:assert/strict";
import { describeExposed, failOver, keepPersistedGroups, listTaggedGroups } from "./kv";
import type { Cli } from "./store";

type Answer = string | ((args: string[]) => string);

function cliAnswering(answers: Record<string, Answer>): { cli: Cli; asked: string[][] } {
  const asked: string[][] = [];
  const cli: Cli = async (args) => {
    asked.push(args);
    const key = args.slice(0, 2).join(" ");
    const answer = answers[key];
    if (answer === undefined) {
      throw new Error(`nothing answers ${args.join(" ")}`);
    }
    return typeof answer === "string" ? answer : answer(args);
  };
  return { cli, asked };
}

function clock() {
  let at = Date.parse("2026-10-01T12:00:00Z");
  return {
    timeoutMs: 600_000,
    intervalMs: 15_000,
    now: () => at,
    sleep: async (ms: number) => {
      at += ms;
    },
  };
}

function taggedGroup(id: string, store: string) {
  return {
    ResourceARN: `arn:aws:elasticache:us-east-1:111122223333:replicationgroup:${id}`,
    Tags: [
      { Key: "ocel:project", Value: "kv" },
      { Key: "ocel:resource", Value: store },
    ],
  };
}

const TAGGED = JSON.stringify({
  ResourceTagMappingList: [
    taggedGroup("ocel-app-ocj-run-kv-production-c-1a2b3c4d", "cache"),
    taggedGroup("ocel-app-ocj-run-kv-production-b-5e6f7a8b", "bounded"),
  ],
});

function described(args: string[]): string {
  const id = args[args.indexOf("--replication-group-id") + 1];
  return JSON.stringify({
    ReplicationGroups: [
      {
        ReplicationGroupId: id,
        Description: "kv / production / infra - a kv store",
        Status: "available",
      },
    ],
  });
}

describe("listTaggedGroups", () => {
  it("finds the replication groups tagged with the cell's project and the store each holds", async () => {
    const { cli, asked } = cliAnswering({
      "resourcegroupstaggingapi get-resources": TAGGED,
    });

    const groups = await listTaggedGroups(cli, "kv");

    assert.deepEqual(groups, [
      { id: "ocel-app-ocj-run-kv-production-c-1a2b3c4d", store: "cache" },
      { id: "ocel-app-ocj-run-kv-production-b-5e6f7a8b", store: "bounded" },
    ]);
    const tagged = asked.find((args) => args[0] === "resourcegroupstaggingapi") ?? [];
    assert.ok(tagged.includes("elasticache:replicationgroup"));
    assert.ok(tagged.includes("Key=ocel:project,Values=kv"));
  });
});

describe("keepPersistedGroups", () => {
  it("keeps only the stores the persistence check reads, by the store tag rather than the id", () => {
    const groups = [
      { id: "a", store: "cache" },
      { id: "b", store: "bounded" },
      { id: "c", store: "evicting" },
      { id: "ocel-app-x-production-cache", store: "my-cache" },
    ];
    assert.deepEqual(
      keepPersistedGroups(groups).map((group) => group.id),
      ["a"],
    );
  });
});

describe("failOver", () => {
  it("tests failover and waits for the group to be available after the failover completed", async () => {
    let polls = 0;
    const { cli, asked } = cliAnswering({
      "elasticache test-failover": "{}",
      "elasticache describe-replication-groups": () => {
        polls += 1;
        return JSON.stringify({
          ReplicationGroups: [{ Status: polls < 3 ? "modifying" : "available" }],
        });
      },
      "elasticache describe-events": () =>
        JSON.stringify({
          Events:
            polls < 2
              ? [{ Message: "Test Failover API called for node group 0001" }]
              : [
                  {
                    Message:
                      "Failover from primary node ocel-app-kv-001 to replica node ocel-app-kv-002 completed",
                  },
                ],
        }),
    });

    const said = await failOver(cli, "ocel-app-kv-production-cache", clock());

    assert.deepEqual(asked[0], [
      "elasticache",
      "test-failover",
      "--replication-group-id",
      "ocel-app-kv-production-cache",
      "--node-group-id",
      "0001",
      "--output",
      "json",
    ]);
    const events = asked.find((args) => args[1] === "describe-events") ?? [];
    assert.ok(
      events.includes("2026-10-01T12:00:00.000Z"),
      "the events read start where the failover began",
    );
    assert.match(said, /completed/);
  });

  it("gives up when no failover completes before the deadline", async () => {
    const { cli } = cliAnswering({
      "elasticache test-failover": "{}",
      "elasticache describe-replication-groups": JSON.stringify({
        ReplicationGroups: [{ Status: "available" }],
      }),
      "elasticache describe-events": JSON.stringify({ Events: [] }),
    });

    await assert.rejects(
      failOver(cli, "ocel-app-kv-production-cache", clock()),
      /no failover of ocel-app-kv-production-cache completed/,
    );
  });
});

describe("describeExposed", () => {
  it("reads the cell's task definitions and replication groups", async () => {
    const { cli, asked } = cliAnswering({
      "resourcegroupstaggingapi get-resources": (args) =>
        args.includes("ecs:task-definition")
          ? JSON.stringify({
              ResourceTagMappingList: [
                { ResourceARN: "arn:aws:ecs:us-east-1:111122223333:task-definition/kv-web:3" },
              ],
            })
          : TAGGED,
      "ecs describe-task-definition": JSON.stringify({
        taskDefinition: {
          containerDefinitions: [
            {
              environment: [{ name: "PORT", value: "8080" }],
              secrets: [{ name: "OCEL_VARIABLES", valueFrom: "arn:aws:ssm:x" }],
            },
          ],
        },
      }),
      "elasticache describe-replication-groups": described,
    });

    const exposed = await describeExposed(cli, "kv");

    assert.match(exposed, /PORT/);
    assert.match(exposed, /OCEL_VARIABLES/);
    assert.match(exposed, /"ReplicationGroupId":"ocel-app-ocj-run-kv-production-c-1a2b3c4d"/);
    assert.ok(
      asked.some((args) =>
        args.includes("arn:aws:ecs:us-east-1:111122223333:task-definition/kv-web:3"),
      ),
    );
  });
});

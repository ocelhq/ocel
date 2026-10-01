import type { Cli } from "./store";

const PROJECT_TAG = "ocel:project";

const PERSISTED_STORES = ["cache"];

const NODE_GROUP = "0001";

const FAILOVER_COMPLETED = /^Failover from .* to replica node .* completed/i;

export type Group = { id: string; description: string };

export type FailoverWait = {
  timeoutMs: number;
  intervalMs: number;
  now: () => number;
  sleep: (ms: number) => Promise<void>;
};

type TaggedPage = {
  ResourceTagMappingList?: Array<{ ResourceARN?: string }>;
  PaginationToken?: string;
};

type DescribedGroups = {
  ReplicationGroups?: Array<{ ReplicationGroupId?: string; Description?: string; Status?: string }>;
};

async function taggedARNs(cli: Cli, resourceType: string, project: string): Promise<string[]> {
  const found: string[] = [];
  let token = "";
  do {
    const page = JSON.parse(
      await cli([
        "resourcegroupstaggingapi",
        "get-resources",
        "--resource-type-filters",
        resourceType,
        "--tag-filters",
        `Key=${PROJECT_TAG},Values=${project}`,
        ...(token ? ["--pagination-token", token] : []),
        "--output",
        "json",
      ]),
    ) as TaggedPage;
    for (const resource of page.ResourceTagMappingList ?? []) {
      if (resource.ResourceARN) {
        found.push(resource.ResourceARN);
      }
    }
    token = page.PaginationToken ?? "";
  } while (token);
  return found;
}

async function describeGroup(cli: Cli, id: string): Promise<string> {
  return cli([
    "elasticache",
    "describe-replication-groups",
    "--replication-group-id",
    id,
    "--output",
    "json",
  ]);
}

export async function taggedGroups(cli: Cli, project: string): Promise<Group[]> {
  const groups: Group[] = [];
  for (const arn of await taggedARNs(cli, "elasticache:replicationgroup", project)) {
    const id = arn.slice(arn.lastIndexOf(":") + 1);
    const described = JSON.parse(await describeGroup(cli, id)) as DescribedGroups;
    groups.push({ id, description: described.ReplicationGroups?.[0]?.Description ?? "" });
  }
  return groups;
}

export function persistedGroups(groups: Group[]): Group[] {
  return groups.filter((group) =>
    PERSISTED_STORES.some((store) => group.description.endsWith(` the ${store} kv store`)),
  );
}

export async function failOver(cli: Cli, id: string, wait: FailoverWait): Promise<string> {
  const began = wait.now();
  const since = new Date(began).toISOString();
  await cli([
    "elasticache",
    "test-failover",
    "--replication-group-id",
    id,
    "--node-group-id",
    NODE_GROUP,
    "--output",
    "json",
  ]);
  let status = "unread";
  while (wait.now() - began < wait.timeoutMs) {
    await wait.sleep(wait.intervalMs);
    const described = JSON.parse(await describeGroup(cli, id)) as DescribedGroups;
    status = described.ReplicationGroups?.[0]?.Status ?? "unread";
    const events = JSON.parse(
      await cli([
        "elasticache",
        "describe-events",
        "--source-type",
        "replication-group",
        "--source-identifier",
        id,
        "--start-time",
        since,
        "--output",
        "json",
      ]),
    ) as { Events?: Array<{ Message?: string }> };
    const completed = (events.Events ?? []).find((event) =>
      FAILOVER_COMPLETED.test(event.Message ?? ""),
    );
    if (completed && status === "available") {
      return `${id}: ${completed.Message}`;
    }
  }
  throw new Error(
    `no failover of ${id} completed within ${wait.timeoutMs} ms of the test-failover call; the group was last ${status}`,
  );
}

export async function exposedOf(cli: Cli, project: string): Promise<string> {
  const shown: string[] = [];
  for (const arn of await taggedARNs(cli, "ecs:task-definition", project)) {
    const described = JSON.parse(
      await cli(["ecs", "describe-task-definition", "--task-definition", arn, "--output", "json"]),
    ) as {
      taskDefinition?: {
        containerDefinitions?: Array<{ environment?: unknown; secrets?: unknown }>;
      };
    };
    for (const container of described.taskDefinition?.containerDefinitions ?? []) {
      shown.push(
        JSON.stringify({ environment: container.environment, secrets: container.secrets }),
      );
    }
  }
  for (const group of await taggedGroups(cli, project)) {
    shown.push(await describeGroup(cli, group.id));
  }
  return shown.join("\n");
}

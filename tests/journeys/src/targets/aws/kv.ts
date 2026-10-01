import type { Cli } from "./store";

const PROJECT_TAG = "ocel:project";

const STORE_TAG = "ocel:resource";

const PERSISTED_STORES = ["cache"];

const NODE_GROUP = "0001";

const FAILOVER_COMPLETED = /^Failover from .* to replica node .* completed/i;

export type Group = { id: string; store: string };

export type FailoverWait = {
  timeoutMs: number;
  intervalMs: number;
  now: () => number;
  sleep: (ms: number) => Promise<void>;
};

type Tagged = { ResourceARN?: string; Tags?: Array<{ Key?: string; Value?: string }> };

type TaggedPage = {
  ResourceTagMappingList?: Tagged[];
  PaginationToken?: string;
};

type DescribedGroups = {
  ReplicationGroups?: Array<{ ReplicationGroupId?: string; Status?: string }>;
};

async function listTagged(cli: Cli, resourceType: string, project: string): Promise<Tagged[]> {
  const found: Tagged[] = [];
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
    found.push(...(page.ResourceTagMappingList ?? []).filter((resource) => resource.ResourceARN));
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

export async function listTaggedGroups(cli: Cli, project: string): Promise<Group[]> {
  return (await listTagged(cli, "elasticache:replicationgroup", project)).map((resource) => {
    const arn = resource.ResourceARN ?? "";
    return {
      id: arn.slice(arn.lastIndexOf(":") + 1),
      store: resource.Tags?.find((tag) => tag.Key === STORE_TAG)?.Value ?? "",
    };
  });
}

export function keepPersistedGroups(groups: Group[]): Group[] {
  return groups.filter((group) => PERSISTED_STORES.includes(group.store));
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

export async function describeExposed(cli: Cli, project: string): Promise<string> {
  const shown: string[] = [];
  for (const { ResourceARN: arn = "" } of await listTagged(cli, "ecs:task-definition", project)) {
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
  for (const group of await listTaggedGroups(cli, project)) {
    shown.push(await describeGroup(cli, group.id));
  }
  return shown.join("\n");
}

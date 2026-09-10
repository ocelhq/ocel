import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { bootstrapStackOf, namespaceOf } from "./namespace";

const run = promisify(execFile);

const TIMEOUT_MS = 60_000;

const RETRYING = { AWS_RETRY_MODE: "adaptive", AWS_MAX_ATTEMPTS: "6" };

const PRODUCTION_PARTITIONS = ["projects#production", "edgestacks#production"];

export type Cli = (args: string[]) => Promise<string>;

export type Store = {
  callerAccount(): Promise<string>;
  deployedSlugs(): Promise<string[]>;
  stands(slug: string): Promise<boolean>;
};

export function cliAt(endpoint: string | undefined): Cli {
  return async (args) => {
    const { stdout } = await run(
      "aws",
      [...(endpoint ? ["--endpoint-url", endpoint] : []), ...args],
      {
        timeout: TIMEOUT_MS,
        maxBuffer: 16 * 1024 * 1024,
        env: { ...process.env, ...RETRYING },
      },
    );
    return stdout.trim();
  };
}

export function said(error: unknown): string {
  const stderr = (error as { stderr?: unknown })?.stderr;
  return `${typeof stderr === "string" ? stderr : ""}\n${String(error)}`;
}

function noSuchStack(error: unknown): boolean {
  return /does not exist/i.test(said(error));
}

function slugOf(sk: string): string {
  return sk.endsWith("#") ? sk.slice(0, -1) : sk;
}

type Page = { Items?: Array<Record<string, unknown>>; NextToken?: string };

async function queryPartition(
  cli: Cli,
  table: string,
  pk: string,
  skPrefix?: string,
): Promise<Array<Record<string, unknown>>> {
  const found: Array<Record<string, unknown>> = [];
  let start: string | undefined;
  do {
    const raw = await cli([
      "dynamodb",
      "query",
      "--table-name",
      table,
      "--consistent-read",
      "--key-condition-expression",
      skPrefix === undefined ? "pk = :pk" : "pk = :pk AND begins_with(sk, :sk)",
      "--expression-attribute-values",
      JSON.stringify(
        skPrefix === undefined
          ? { ":pk": { S: pk } }
          : { ":pk": { S: pk }, ":sk": { S: skPrefix } },
      ),
      ...(start ? ["--starting-token", start] : []),
      "--output",
      "json",
    ]);
    const page = JSON.parse(raw) as Page;
    found.push(...(page.Items ?? []));
    start = page.NextToken;
  } while (start);
  return found;
}

const STATE_TABLE_OUTPUT = "StateTableName";

async function bootstrapTable(
  cli: Cli,
  stack: string,
  output: string,
  consequence: string,
): Promise<string | undefined> {
  let name: string;
  try {
    name = await cli([
      "cloudformation",
      "describe-stacks",
      "--stack-name",
      stack,
      "--query",
      `Stacks[0].Outputs[?OutputKey=='${output}']|[0].OutputValue`,
      "--output",
      "text",
    ]);
  } catch (error) {
    if (noSuchStack(error)) {
      return undefined;
    }
    throw new Error(`the ${stack} stack could not be read, so ${consequence}:${said(error)}`);
  }
  if (name === "" || name === "None") {
    throw new Error(
      `the ${stack} stack stands but publishes no ${output} output, so ${consequence}`,
    );
  }
  return name;
}

async function stateTable(cli: Cli, stack: string): Promise<string | undefined> {
  return bootstrapTable(
    cli,
    stack,
    STATE_TABLE_OUTPUT,
    "nothing can be said about which projects stand",
  );
}

export function awsStore(
  endpoint?: string,
  cli: Cli = cliAt(endpoint),
  namespace: string = namespaceOf(process.env),
): Store {
  const stack = bootstrapStackOf(namespace);

  async function query(table: string, partition: string, slug?: string): Promise<string[]> {
    const found: string[] = [];
    for (const item of await queryPartition(cli, table, partition, slug)) {
      const one = slugOf((item.sk as { S?: string } | undefined)?.S ?? "");
      if (one !== "") {
        found.push(one);
      }
    }
    return found;
  }

  return {
    async callerAccount() {
      return cli(["sts", "get-caller-identity", "--query", "Account", "--output", "text"]);
    },

    async deployedSlugs() {
      const table = await stateTable(cli, stack);
      if (!table) {
        return [];
      }
      const found = new Set<string>();
      for (const partition of PRODUCTION_PARTITIONS) {
        for (const slug of await query(table, partition)) {
          found.add(slug);
        }
      }
      return [...found];
    },

    async stands(slug) {
      const table = await stateTable(cli, stack);
      if (!table) {
        return false;
      }
      for (const partition of PRODUCTION_PARTITIONS) {
        if ((await query(table, partition, slug)).includes(slug)) {
          return true;
        }
      }
      return false;
    },
  };
}

const BINDING_CLASS = "production";

const BINDING_TYPES = ["postgres", "bucket", "custom"] as const;

export type BindingGrant = { actions: string[]; resources: string[]; label?: string };
export type BindingKind = (typeof BINDING_TYPES)[number] | "unspecified";

export type BindingRecordItem = {
  name: string;
  type: BindingKind;
  source: string;
  owner: string;
  grants: BindingGrant[];
  redactedProperties: Record<string, unknown>;
};

export type BindingValueItem = { name: string; sealed: string };

export type BindingStore = {
  records(slug: string): Promise<BindingRecordItem[]>;
  values(slug: string): Promise<BindingValueItem[]>;
  ownerIndex(slug: string, owner: string): Promise<string[] | undefined>;
};

function bindingsPartition(slug: string): string {
  return `values#${slug}#${BINDING_CLASS}`;
}

type RawItem = { sk: string; body: string };

async function queryItems(
  cli: Cli,
  table: string,
  pk: string,
  skPrefix: string,
): Promise<RawItem[]> {
  const found: RawItem[] = [];
  for (const item of await queryPartition(cli, table, pk, skPrefix)) {
    const sk = (item.sk as { S?: string } | undefined)?.S;
    const body = (item.body as { B?: string } | undefined)?.B;
    if (sk && body) {
      found.push({ sk, body: Buffer.from(body, "base64").toString("utf8") });
    }
  }
  return found;
}

function bindingTypeOf(binding: Record<string, unknown>): BindingKind {
  return BINDING_TYPES.find((type) => type in binding) ?? "unspecified";
}

export function awsBindingStore(
  endpoint?: string,
  cli: Cli = cliAt(endpoint),
  namespace: string = namespaceOf(process.env),
): BindingStore {
  const stack = bootstrapStackOf(namespace);

  async function bindingTableOrThrow(): Promise<string> {
    const name = await bootstrapTable(cli, stack, STATE_TABLE_OUTPUT, "no binding can be read");
    if (!name) {
      throw new Error(
        `the ${stack} stack publishes no ${STATE_TABLE_OUTPUT} output, so no binding can be read`,
      );
    }
    return name;
  }

  async function bindingItems(slug: string): Promise<RawItem[]> {
    return queryItems(cli, await bindingTableOrThrow(), bindingsPartition(slug), "bindings#");
  }

  return {
    async records(slug) {
      const records: BindingRecordItem[] = [];
      for (const item of await bindingItems(slug)) {
        const [, , kind] = item.sk.split("#");
        if (kind !== "records") {
          continue;
        }
        const envelope = JSON.parse(item.body) as { record: string; owner?: string };
        const binding = JSON.parse(
          Buffer.from(envelope.record, "base64").toString("utf8"),
        ) as Record<string, unknown> & { name: string; source?: string; grants?: BindingGrant[] };
        const type = bindingTypeOf(binding);
        records.push({
          name: binding.name,
          type,
          source: binding.source ?? "",
          owner: envelope.owner || "OCEL",
          grants: binding.grants ?? [],
          redactedProperties: (binding[type] as Record<string, unknown> | undefined) ?? {},
        });
      }
      return records;
    },

    async values(slug) {
      const values: BindingValueItem[] = [];
      for (const item of await bindingItems(slug)) {
        const [, name, kind] = item.sk.split("#");
        if (kind !== "values" || !name) {
          continue;
        }
        const envelope = JSON.parse(item.body) as { sealed: string };
        values.push({ name, sealed: envelope.sealed });
      }
      return values;
    },

    async ownerIndex(slug, owner) {
      const items = await queryItems(
        cli,
        await bindingTableOrThrow(),
        bindingsPartition(slug),
        `bindingowners#${owner}#`,
      );
      const [item] = items;
      if (!item) {
        return undefined;
      }
      const envelope = JSON.parse(item.body) as { names?: string[] };
      return envelope.names ?? [];
    },
  };
}

const NAMESPACE_TAG = "ocel:namespace";

type TaggedPage = {
  ResourceTagMappingList?: Array<{ Tags?: Array<{ Key?: string; Value?: string }> }>;
  PaginationToken?: string;
};

export async function namespacesStanding(cli: Cli): Promise<string[]> {
  const found: string[] = [];
  const seen = new Set<string>();
  let token = "";
  do {
    const raw = await cli([
      "resourcegroupstaggingapi",
      "get-resources",
      "--resource-type-filters",
      "cloudformation:stack",
      "--tag-filters",
      `Key=${NAMESPACE_TAG}`,
      ...(token ? ["--pagination-token", token] : []),
      "--output",
      "json",
    ]);
    const page = JSON.parse(raw) as TaggedPage;
    for (const resource of page.ResourceTagMappingList ?? []) {
      const named = resource.Tags?.find((tag) => tag.Key === NAMESPACE_TAG)?.Value;
      if (named && !seen.has(named)) {
        seen.add(named);
        found.push(named);
      }
    }
    token = page.PaginationToken ?? "";
  } while (token);
  return found;
}

export async function answersAsFloci(endpoint: string): Promise<boolean> {
  try {
    const res = await fetch(`${endpoint.replace(/\/$/, "")}/_localstack/health`, {
      signal: AbortSignal.timeout(5_000),
    });
    if (!res.ok) {
      return false;
    }
    const health = (await res.json()) as { services?: Record<string, unknown> };
    return "cloudformation" in (health.services ?? health);
  } catch {
    return false;
  }
}

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

const LINK_CLASS = "production";

const LINK_TYPES = ["postgres", "bucket", "custom"] as const;

export type LinkGrant = { actions: string[]; resources: string[]; label?: string };
export type LinkKind = (typeof LINK_TYPES)[number] | "unspecified";

export type LinkRecordItem = {
  name: string;
  type: LinkKind;
  source: string;
  owner: string;
  grants: LinkGrant[];
  redactedProperties: Record<string, unknown>;
};

export type LinkValueItem = { name: string; sealed: string };

export type LinkStore = {
  records(slug: string): Promise<LinkRecordItem[]>;
  values(slug: string): Promise<LinkValueItem[]>;
  ownerIndex(slug: string, owner: string): Promise<string[] | undefined>;
};

function linksPartition(slug: string): string {
  return `values#${slug}#${LINK_CLASS}`;
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

function linkTypeOf(link: Record<string, unknown>): LinkKind {
  return LINK_TYPES.find((type) => type in link) ?? "unspecified";
}

export function awsLinkStore(
  endpoint?: string,
  cli: Cli = cliAt(endpoint),
  namespace: string = namespaceOf(process.env),
): LinkStore {
  const stack = bootstrapStackOf(namespace);

  async function linkTableOrThrow(): Promise<string> {
    const name = await bootstrapTable(cli, stack, STATE_TABLE_OUTPUT, "no link can be read");
    if (!name) {
      throw new Error(
        `the ${stack} stack publishes no ${STATE_TABLE_OUTPUT} output, so no link can be read`,
      );
    }
    return name;
  }

  async function linkItems(slug: string): Promise<RawItem[]> {
    return queryItems(cli, await linkTableOrThrow(), linksPartition(slug), "links#");
  }

  return {
    async records(slug) {
      const records: LinkRecordItem[] = [];
      for (const item of await linkItems(slug)) {
        const [, , kind] = item.sk.split("#");
        if (kind !== "records") {
          continue;
        }
        const envelope = JSON.parse(item.body) as { record: string; owner?: string };
        const link = JSON.parse(Buffer.from(envelope.record, "base64").toString("utf8")) as Record<
          string,
          unknown
        > & { name: string; source?: string; grants?: LinkGrant[] };
        const type = linkTypeOf(link);
        records.push({
          name: link.name,
          type,
          source: link.source ?? "",
          owner: envelope.owner || "OCEL",
          grants: link.grants ?? [],
          redactedProperties: (link[type] as Record<string, unknown> | undefined) ?? {},
        });
      }
      return records;
    },

    async values(slug) {
      const values: LinkValueItem[] = [];
      for (const item of await linkItems(slug)) {
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
        await linkTableOrThrow(),
        linksPartition(slug),
        `linkowners#${owner}#`,
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

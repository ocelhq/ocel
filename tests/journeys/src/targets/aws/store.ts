import { execFile } from "node:child_process";
import { promisify } from "node:util";

const run = promisify(execFile);

const TIMEOUT_MS = 60_000;

const RETRYING = { AWS_RETRY_MODE: "adaptive", AWS_MAX_ATTEMPTS: "6" };

const BOOTSTRAP_STACK = "ocel-bootstrap";

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

export async function varsTable(cli: Cli): Promise<string | undefined> {
  let name: string;
  try {
    name = await cli([
      "cloudformation",
      "describe-stacks",
      "--stack-name",
      BOOTSTRAP_STACK,
      "--query",
      "Stacks[0].Outputs[?OutputKey=='VarsTableName']|[0].OutputValue",
      "--output",
      "text",
    ]);
  } catch (error) {
    if (noSuchStack(error)) {
      return undefined;
    }
    throw new Error(
      `the ${BOOTSTRAP_STACK} stack could not be read, so nothing can be said about which projects stand:${said(error)}`,
    );
  }
  if (name === "" || name === "None") {
    throw new Error(
      `the ${BOOTSTRAP_STACK} stack stands but publishes no VarsTableName output, so nothing can be said about which projects stand`,
    );
  }
  return name;
}

export function awsStore(endpoint?: string, cli: Cli = cliAt(endpoint)): Store {
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
      const table = await varsTable(cli);
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
      const table = await varsTable(cli);
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

async function queryItems(cli: Cli, table: string, pk: string, skPrefix: string): Promise<RawItem[]> {
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

export function awsLinkStore(endpoint?: string, cli: Cli = cliAt(endpoint)): LinkStore {
  async function varsTableOrThrow(): Promise<string> {
    const name = await varsTable(cli);
    if (!name) {
      throw new Error(
        `the ${BOOTSTRAP_STACK} stack publishes no VarsTableName output, so no link can be read`,
      );
    }
    return name;
  }

  async function linkItems(slug: string): Promise<RawItem[]> {
    return queryItems(cli, await varsTableOrThrow(), linksPartition(slug), "links#");
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
        await varsTableOrThrow(),
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

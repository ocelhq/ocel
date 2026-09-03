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

type Page = { Items?: Array<{ sk?: { S?: string } }>; NextToken?: string };

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
    let start: string | undefined;
    do {
      const raw = await cli([
        "dynamodb",
        "query",
        "--table-name",
        table,
        "--consistent-read",
        "--key-condition-expression",
        slug === undefined ? "pk = :pk" : "pk = :pk AND begins_with(sk, :sk)",
        "--expression-attribute-values",
        JSON.stringify(
          slug === undefined
            ? { ":pk": { S: partition } }
            : { ":pk": { S: partition }, ":sk": { S: slug } },
        ),
        ...(start ? ["--starting-token", start] : []),
        "--output",
        "json",
      ]);
      const page = JSON.parse(raw) as Page;
      for (const item of page.Items ?? []) {
        const one = slugOf(item.sk?.S ?? "");
        if (one !== "") {
          found.push(one);
        }
      }
      start = page.NextToken;
    } while (start);
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

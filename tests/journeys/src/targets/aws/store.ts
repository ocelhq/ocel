import { execFile } from "node:child_process";
import { promisify } from "node:util";

const run = promisify(execFile);

const TIMEOUT_MS = 60_000;

const RETRYING = { AWS_RETRY_MODE: "adaptive", AWS_MAX_ATTEMPTS: "6" };

const BOOTSTRAP_STACK = "ocel-bootstrap";

const PRODUCTION_PARTITIONS = ["projects#production", "edgestacks#production"];

async function aws(endpoint: string | undefined, args: string[]): Promise<string> {
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
}

export async function callerAccount(endpoint: string | undefined): Promise<string> {
  return aws(endpoint, ["sts", "get-caller-identity", "--query", "Account", "--output", "text"]);
}

async function varsTable(endpoint: string | undefined): Promise<string | undefined> {
  try {
    const name = await aws(endpoint, [
      "cloudformation",
      "describe-stacks",
      "--stack-name",
      BOOTSTRAP_STACK,
      "--query",
      "Stacks[0].Outputs[?OutputKey=='VarsTableName']|[0].OutputValue",
      "--output",
      "text",
    ]);
    return name === "" || name === "None" ? undefined : name;
  } catch {
    return undefined;
  }
}

async function partitionSlugs(
  endpoint: string | undefined,
  table: string,
  partition: string,
): Promise<string[]> {
  const slugs: string[] = [];
  let start: string | undefined;
  do {
    const raw = await aws(endpoint, [
      "dynamodb",
      "query",
      "--table-name",
      table,
      "--consistent-read",
      "--key-condition-expression",
      "pk = :pk",
      "--expression-attribute-values",
      JSON.stringify({ ":pk": { S: partition } }),
      ...(start ? ["--starting-token", start] : []),
      "--output",
      "json",
    ]);
    const page = JSON.parse(raw) as {
      Items?: Array<{ sk?: { S?: string } }>;
      NextToken?: string;
    };
    for (const item of page.Items ?? []) {
      const sk = item.sk?.S ?? "";
      const slug = sk.endsWith("#") ? sk.slice(0, -1) : sk;
      if (slug !== "") {
        slugs.push(slug);
      }
    }
    start = page.NextToken;
  } while (start);
  return slugs;
}

export async function deployedSlugs(endpoint: string | undefined): Promise<string[]> {
  const table = await varsTable(endpoint);
  if (!table) {
    return [];
  }
  const found = new Set<string>();
  for (const partition of PRODUCTION_PARTITIONS) {
    for (const slug of await partitionSlugs(endpoint, table, partition)) {
      found.add(slug);
    }
  }
  return [...found];
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

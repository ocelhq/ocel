import { execFileSync } from "node:child_process";
import path from "node:path";
import { provisionPulumi, removePulumi } from "./aws-links/pulumi";
import { provisionSst, removeSst } from "./aws-links/sst";
import { examples } from "./examples";
import {
  customLinkName,
  grantsDeliveredProblem,
  linkIndexSortKey,
  linkName,
  linkOwner,
  linkPartitionKey,
  linkRecord,
  listedLinkProblem,
  pairProblem,
  publishedRecordProblem,
  recordSortKey,
  valueSortKey,
  varsReachProblem,
  vpcPlacementProblem,
  type LinkExpectation,
  type LinkOutputs,
  type LinkSource,
  type StoreRow,
} from "./links";
import { successful } from "./process";
import type { Example, LinkReport, LinkTool } from "./types";

const timeoutMs = 45 * 60_000;
const vpcAccessPolicy =
  "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole";

type FunctionConfiguration = {
  FunctionName?: string;
  Role?: string;
  Environment?: { Variables?: Record<string, string> };
  VpcConfig?: { SubnetIds?: string[]; SecurityGroupIds?: string[] };
};

export type ExternalLinks = {
  readonly outputs: LinkOutputs;
  readonly report: LinkReport;
  provision: () => Promise<void>;
  teardown: () => Promise<void>;
  assertPublished: () => Promise<void>;
  assertConsumed: (appName: string) => Promise<void>;
  assertConsumerRemoved: () => void;
  assertPublisherRemoved: () => void;
};

function aws(args: string[]) {
  return execFileSync("aws", args, {
    encoding: "utf8",
    timeout: 30_000,
    maxBuffer: 16 * 1024 * 1024,
    env: { ...process.env, AWS_RETRY_MODE: "standard", AWS_MAX_ATTEMPTS: "4" },
  }).trim();
}

function varsTable() {
  const table = aws([
    "cloudformation",
    "describe-stacks",
    "--stack-name",
    "ocel-bootstrap",
    "--query",
    "Stacks[0].Outputs[?OutputKey=='VarsTableName'].OutputValue",
    "--output",
    "text",
  ]);
  if (!table) throw new Error("the AWS account has no ocel vars table");
  return table;
}

function partitionRows(table: string, partitionKey: string) {
  const result = JSON.parse(
    aws([
      "dynamodb",
      "query",
      "--table-name",
      table,
      "--consistent-read",
      "--key-condition-expression",
      "pk = :pk",
      "--expression-attribute-values",
      JSON.stringify({ ":pk": { S: partitionKey } }),
      "--output",
      "json",
    ]),
  ) as { Items?: StoreRow[] };
  return Object.fromEntries(
    (result.Items ?? []).flatMap((row) =>
      row.sk?.S ? [[row.sk.S, row] as const] : [],
    ),
  );
}

function item(table: string, partitionKey: string, sortKey: string) {
  const result = JSON.parse(
    aws([
      "dynamodb",
      "get-item",
      "--table-name",
      table,
      "--consistent-read",
      "--key",
      JSON.stringify({ pk: { S: partitionKey }, sk: { S: sortKey } }),
      "--output",
      "json",
    ]),
  ) as { Item?: StoreRow };
  return result.Item;
}

function taggedFunctionArns(slug: string, appName: string) {
  const result = JSON.parse(
    aws([
      "resourcegroupstaggingapi",
      "get-resources",
      "--resource-type-filters",
      "lambda:function",
      "--tag-filters",
      `Key=ocel:project,Values=${slug}`,
      `Key=ocel:app,Values=${appName}`,
      "--output",
      "json",
    ]),
  ) as { ResourceTagMappingList?: Array<{ ResourceARN?: string }> };
  return (result.ResourceTagMappingList ?? []).flatMap((entry) =>
    entry.ResourceARN ? [entry.ResourceARN] : [],
  );
}

function functionConfiguration(arn: string) {
  return JSON.parse(
    aws([
      "lambda",
      "get-function-configuration",
      "--function-name",
      arn,
      "--output",
      "json",
    ]),
  ) as FunctionConfiguration;
}

function roleName(roleArn?: string) {
  const name = roleArn?.split("/").pop();
  if (!name) throw new Error(`Lambda has no execution role in ${roleArn ?? "nothing"}`);
  return name;
}

function roleInlinePolicies(name: string) {
  const listed = JSON.parse(
    aws(["iam", "list-role-policies", "--role-name", name, "--output", "json"]),
  ) as { PolicyNames?: string[] };
  return (listed.PolicyNames ?? []).map((policy) => {
    const result = JSON.parse(
      aws([
        "iam",
        "get-role-policy",
        "--role-name",
        name,
        "--policy-name",
        policy,
        "--output",
        "json",
      ]),
    ) as { PolicyDocument: Record<string, unknown> };
    return result.PolicyDocument;
  });
}

function attachedPolicyArns(name: string) {
  const result = JSON.parse(
    aws([
      "iam",
      "list-attached-role-policies",
      "--role-name",
      name,
      "--output",
      "json",
    ]),
  ) as { AttachedPolicies?: Array<{ PolicyArn?: string }> };
  return (result.AttachedPolicies ?? []).flatMap((policy) =>
    policy.PolicyArn ? [policy.PolicyArn] : [],
  );
}

function requireValid(label: string, problem: string | null) {
  if (problem) throw new Error(`${label}: ${problem}`);
}

function stackToken() {
  const raw = process.env.GITHUB_RUN_ID ?? "local";
  const token = raw.toLowerCase().replace(/[^a-z0-9]+/g, "").slice(-20);
  return `e2e-${token || "local"}`;
}

function baseEnvironment(example: Example, slug: string, environment: string) {
  return {
    ...process.env,
    OCEL_CONFIG: "ocel.aws.config.ts",
    OCEL_LINK_ENVIRONMENT: environment,
    OCEL_LINK_PROJECT: example.dir,
    OCEL_TEST_PROJECT_SLUG: slug,
  };
}

async function listedLinks(example: Example, env: NodeJS.ProcessEnv, environment: string) {
  const result = await successful(
    "ocel link ls",
    process.execPath,
    [
      path.join(example.dir, "node_modules", "ocel", "bin", "run.js"),
      "link",
      "ls",
      "--log-format",
      "json",
      "--preview",
      "--environment",
      environment,
    ],
    { cwd: example.dir, env, timeoutMs },
  );
  const start = result.stdout.indexOf("{");
  if (start < 0) throw new Error("ocel link ls returned no JSON");
  const parsed = JSON.parse(result.stdout.slice(start)) as {
    links?: Array<{ name?: string; type?: string; source?: string; owner?: string }>;
  };
  return parsed.links ?? [];
}

function expectations(
  source: LinkSource,
  project: string,
  stack: string,
) {
  return [
    {
      name: linkName,
      type: "postgres",
      source,
      owner: linkOwner(source, project, stack),
    },
    {
      name: customLinkName,
      type: "custom",
      source,
      owner: linkOwner(source, project, stack, customLinkName),
    },
  ] satisfies LinkExpectation[];
}

function records(
  table: string,
  slug: string,
  environment: string,
  expected: LinkExpectation,
) {
  const rows = partitionRows(table, linkPartitionKey(slug, expected.name));
  return {
    record: rows[recordSortKey(environment)],
    value: rows[valueSortKey(environment)],
  };
}

export function provisionExternalLinks(
  example: Example,
  slug: string,
  environment: string,
): ExternalLinks {
  const tool = example.linkTool;
  if (!tool) throw new Error(`${example.name} names no external link tool`);
  const stack = stackToken();
  const env = baseEnvironment(example, slug, environment);
  const expected = expectations(tool, example.name, stack);
  let provisioned: { outputs: LinkOutputs } | undefined;
  let table: string | undefined;
  const ready = () => {
    if (!provisioned) throw new Error(`${tool} external links are not provisioned`);
    return provisioned;
  };
  const tableForFixture = () => (table ??= varsTable());
  return {
    get outputs() {
      return ready().outputs;
    },
    get report() {
      const published = ready().outputs;
      return {
        host: published.host,
        port: published.port,
        database: published.database,
        hasPassword: true,
        connected: true,
      };
    },
    provision: async () => {
      provisioned =
        tool === "sst"
          ? await provisionSst(example, env, stack)
          : await provisionPulumi(example, env, stack);
    },
    teardown: async () => {
      if (tool === "sst") await removeSst(example, env, stack);
      else await removePulumi(example, env, stack);
    },
    assertPublished: async () => {
      ready();
      const links = await listedLinks(example, env, environment);
      const table = tableForFixture();
      for (const expectation of expected) {
        requireValid(
          `${expectation.name} listing`,
          listedLinkProblem(links, expectation),
        );
        const rows = records(table, slug, environment, expectation);
        requireValid(
          `${expectation.name} record pair`,
          pairProblem(rows.record, rows.value),
        );
        requireValid(
          `${expectation.name} record`,
          publishedRecordProblem(rows.record, expectation),
        );
        const index = item(
          table,
          `PROJECT#${slug}#CLASS#preview`,
          linkIndexSortKey(expectation.owner, environment),
        );
        const owned = index?.links?.SS ?? [];
        if (owned.length !== 1 || owned[0] !== expectation.name) {
          throw new Error(
            `${expectation.name} owner index contains ${JSON.stringify(owned)}`,
          );
        }
      }
    },
    assertConsumed: async (appName) => {
      const published = ready();
      const table = tableForFixture();
      const postgres = records(table, slug, environment, expected[0]);
      requireValid(
        "consumed postgres record",
        publishedRecordProblem(postgres.record, expected[0]),
      );
      const custom = records(table, slug, environment, expected[1]);
      requireValid(
        "consumed custom record",
        publishedRecordProblem(custom.record, expected[1]),
      );
      const ocel = item(
        table,
        `PROJECT#${slug}#CLASS#preview`,
        linkIndexSortKey("OCEL", environment),
      );
      const ocelOwned = ocel?.links?.SS ?? [];
      if (ocelOwned.includes(linkName) || ocelOwned.includes(customLinkName)) {
        throw new Error(`ocel claims externally published links ${JSON.stringify(ocelOwned)}`);
      }
      const arns = taggedFunctionArns(slug, appName);
      if (!arns.length) throw new Error(`no Lambda is tagged for ${slug}/${appName}`);
      const configurations = arns.map(functionConfiguration);
      for (const configuration of configurations) {
        const variables = configuration.Environment?.Variables ?? {};
        if (!("OCEL_RESOURCE_POSTGRES_orders" in variables)) {
          throw new Error(`${configuration.FunctionName} has no orders link environment`);
        }
        for (const value of [published.outputs.host, published.outputs.database]) {
          if (Object.values(variables).some((entry) => entry.includes(value))) {
            throw new Error(`${configuration.FunctionName} carries a link value in cleartext`);
          }
        }
        requireValid(
          `${configuration.FunctionName} placement`,
          vpcPlacementProblem(configuration, published.outputs),
        );
      }
      const roles = [...new Set(configurations.map((config) => roleName(config.Role)))];
      for (const role of roles) {
        if (!attachedPolicyArns(role).includes(vpcAccessPolicy)) {
          throw new Error(`${role} has no VPC access policy`);
        }
      }
      const documents = roles.flatMap(roleInlinePolicies);
      const grants = linkRecord(postgres.record)?.grants;
      requireValid("delivered grants", grantsDeliveredProblem(documents, grants));
      requireValid(
        "vars partition access",
        varsReachProblem(documents, linkPartitionKey(slug, linkName)),
      );
    },
    assertConsumerRemoved: () => {
      const row = records(tableForFixture(), slug, environment, expected[0]).record;
      if (!row) throw new Error("consumer removal deleted the publisher's record");
    },
    assertPublisherRemoved: () => {
      const table = tableForFixture();
      for (const expectation of expected) {
        const rows = partitionRows(table, linkPartitionKey(slug, expectation.name));
        if (rows[recordSortKey(environment)] || rows[valueSortKey(environment)]) {
          throw new Error(`${expectation.name} survived external stack removal`);
        }
      }
    },
  };
}

export async function teardownExternalLinks(slug: string) {
  const stack = stackToken();
  const failures: unknown[] = [];
  for (const example of examples) {
    const tool = "linkTool" in example ? (example.linkTool as LinkTool) : undefined;
    const env = baseEnvironment(example, slug, `conformance-${example.name}`);
    try {
      if (tool === "sst") await removeSst(example, env, stack);
      if (tool === "pulumi") await removePulumi(example, env, stack);
    } catch (error) {
      failures.push(error);
    }
  }
  if (failures.length) {
    throw new AggregateError(failures, "external link teardown failed");
  }
}

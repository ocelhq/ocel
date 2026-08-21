import { execFileSync } from "node:child_process";
import { access, mkdir, rm } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";
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
  splitIds,
  valueSortKey,
  varsReachProblem,
  vpcPlacementProblem,
  type LinkExpectation,
  type LinkOutputs,
  type LinkSource,
  type StoreRow,
} from "./links";
import { run, successful } from "./process";
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
  outputs: LinkOutputs;
  report: LinkReport;
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

function pulumiStateDir(example: Example) {
  return path.join(example.dir, ".ocel", "conformance-pulumi");
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

function outputsFromSst(stdout: string): LinkOutputs {
  const values: Record<string, string> = {};
  for (const line of stdout.split("\n")) {
    const match = /^\s*([A-Za-z][A-Za-z0-9_]*)\s*:\s*(\S.*?)\s*$/.exec(line);
    if (match) values[match[1]!] = match[2]!;
  }
  return outputs({
    host: values.host,
    port: values.port,
    database: values.database,
    subnetIds: values.subnetIds,
    securityGroupIds: values.securityGroupIds,
  });
}

function outputs(values: Record<string, unknown>): LinkOutputs {
  for (const name of ["host", "port", "database"] as const) {
    if (values[name] === undefined || values[name] === "") {
      throw new Error(`the external stack returned no ${name}`);
    }
  }
  const subnetIds = splitIds(values.subnetIds);
  const securityGroupIds = splitIds(values.securityGroupIds);
  if (!subnetIds.length || !securityGroupIds.length) {
    throw new Error("the external stack returned no network placement");
  }
  return {
    host: String(values.host),
    port: String(values.port),
    database: String(values.database),
    subnetIds,
    securityGroupIds,
  };
}

async function provisionSst(
  example: Example,
  env: NodeJS.ProcessEnv,
  stack: string,
) {
  await successful(
    "sst deploy",
    "pnpm",
    ["exec", "sst", "deploy", "--stage", stack],
    { cwd: example.dir, env, timeoutMs },
  );
  const result = await successful(
    "sst outputs",
    "pnpm",
    ["exec", "sst", "outputs", "--stage", stack],
    { cwd: example.dir, env, timeoutMs },
  );
  return {
    outputs: outputsFromSst(result.stdout),
    teardown: async () => {
      await successful(
        "sst remove",
        "pnpm",
        ["exec", "sst", "remove", "--stage", stack],
        { cwd: example.dir, env, timeoutMs },
      );
    },
  };
}

function pulumiEnvironment(example: Example, env: NodeJS.ProcessEnv) {
  const state = pulumiStateDir(example);
  const passphrase = process.env.PULUMI_CONFIG_PASSPHRASE;
  if (!passphrase) {
    throw new Error("PULUMI_CONFIG_PASSPHRASE is required by the Pulumi fixture");
  }
  return {
    state,
    env: {
      ...env,
      PULUMI_BACKEND_URL: pathToFileURL(state).href,
      PULUMI_CONFIG_PASSPHRASE: passphrase,
    },
  };
}

async function provisionPulumi(
  example: Example,
  env: NodeJS.ProcessEnv,
  stack: string,
) {
  const local = pulumiEnvironment(example, env);
  await mkdir(local.state, { recursive: true });
  await successful("pulumi login", "pulumi", ["login", local.env.PULUMI_BACKEND_URL], {
    cwd: example.dir,
    env: local.env,
    timeoutMs,
  });
  await successful(
    "pulumi stack init",
    "pulumi",
    ["stack", "init", stack, "--non-interactive"],
    { cwd: example.dir, env: local.env, timeoutMs },
  );
  const region = process.env.AWS_REGION;
  if (!region) throw new Error("AWS_REGION is required by the Pulumi fixture");
  await successful(
    "pulumi aws region",
    "pulumi",
    ["config", "set", "aws:region", region, "--stack", stack, "--non-interactive"],
    { cwd: example.dir, env: local.env, timeoutMs },
  );
  await successful(
    "pulumi database password",
    "pulumi",
    [
      "config",
      "set",
      "--secret",
      "dbPassword",
      crypto.randomUUID().replaceAll("-", ""),
      "--stack",
      stack,
      "--non-interactive",
    ],
    { cwd: example.dir, env: local.env, timeoutMs },
  );
  await successful(
    "pulumi up",
    "pulumi",
    ["up", "--stack", stack, "--yes", "--skip-preview", "--non-interactive"],
    { cwd: example.dir, env: local.env, timeoutMs },
  );
  const result = await successful(
    "pulumi stack output",
    "pulumi",
    ["stack", "output", "--json", "--stack", stack],
    { cwd: example.dir, env: local.env, timeoutMs },
  );
  const parsed = JSON.parse(result.stdout) as Record<string, unknown>;
  return {
    outputs: outputs({
      host: parsed.host,
      port: parsed.port,
      database: parsed.database,
      subnetIds: parsed.publishedSubnetIds,
      securityGroupIds: parsed.publishedSecurityGroupIds,
    }),
    teardown: async () => {
      await successful(
        "pulumi destroy",
        "pulumi",
        ["destroy", "--stack", stack, "--yes", "--skip-preview", "--non-interactive"],
        { cwd: example.dir, env: local.env, timeoutMs },
      );
      await successful(
        "pulumi stack rm",
        "pulumi",
        ["stack", "rm", stack, "--yes", "--non-interactive"],
        { cwd: example.dir, env: local.env, timeoutMs },
      );
      await rm(local.state, { recursive: true, force: true });
    },
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

export async function provisionExternalLinks(
  example: Example,
  slug: string,
  environment: string,
): Promise<ExternalLinks> {
  const tool = example.linkTool;
  if (!tool) throw new Error(`${example.name} names no external link tool`);
  const stack = stackToken();
  const env = baseEnvironment(example, slug, environment);
  const provisioned =
    tool === "sst"
      ? await provisionSst(example, env, stack)
      : await provisionPulumi(example, env, stack);
  const expected = expectations(tool, example.name, stack);
  const report = {
    host: provisioned.outputs.host,
    port: provisioned.outputs.port,
    database: provisioned.outputs.database,
    hasPassword: true,
    connected: true,
  };
  return {
    outputs: provisioned.outputs,
    report,
    teardown: provisioned.teardown,
    assertPublished: async () => {
      const links = await listedLinks(example, env, environment);
      const table = varsTable();
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
      const table = varsTable();
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
        for (const value of [provisioned.outputs.host, provisioned.outputs.database]) {
          if (Object.values(variables).some((entry) => entry.includes(value))) {
            throw new Error(`${configuration.FunctionName} carries a link value in cleartext`);
          }
        }
        requireValid(
          `${configuration.FunctionName} placement`,
          vpcPlacementProblem(configuration, provisioned.outputs),
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
      const row = records(varsTable(), slug, environment, expected[0]).record;
      if (!row) throw new Error("consumer removal deleted the publisher's record");
    },
    assertPublisherRemoved: () => {
      const table = varsTable();
      for (const expectation of expected) {
        const rows = partitionRows(table, linkPartitionKey(slug, expectation.name));
        if (rows[recordSortKey(environment)] || rows[valueSortKey(environment)]) {
          throw new Error(`${expectation.name} survived external stack removal`);
        }
      }
    },
  };
}

async function removeSst(example: Example, slug: string, stack: string) {
  const env = baseEnvironment(example, slug, `conformance-${example.name}`);
  const result = await run("pnpm", ["exec", "sst", "remove", "--stage", stack], {
    cwd: example.dir,
    env,
    timeoutMs,
  });
  if (
    result.code !== 0 &&
    !`${result.stdout}\n${result.stderr}`.toLowerCase().includes("not found")
  ) {
    throw new Error(`sst remove failed\n${result.stdout}\n${result.stderr}`);
  }
}

async function removePulumi(example: Example, slug: string, stack: string) {
  const env = baseEnvironment(example, slug, `conformance-${example.name}`);
  const local = pulumiEnvironment(example, env);
  try {
    await access(local.state);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return;
    throw error;
  }
  await successful("pulumi login", "pulumi", ["login", local.env.PULUMI_BACKEND_URL], {
    cwd: example.dir,
    env: local.env,
    timeoutMs,
  });
  const selected = await run(
    "pulumi",
    ["stack", "select", stack, "--non-interactive"],
    { cwd: example.dir, env: local.env, timeoutMs },
  );
  if (selected.code === 0) {
    await successful(
      "pulumi destroy",
      "pulumi",
      ["destroy", "--stack", stack, "--yes", "--skip-preview", "--non-interactive"],
      { cwd: example.dir, env: local.env, timeoutMs },
    );
    await successful(
      "pulumi stack rm",
      "pulumi",
      ["stack", "rm", stack, "--yes", "--non-interactive"],
      { cwd: example.dir, env: local.env, timeoutMs },
    );
  }
  await rm(local.state, { recursive: true, force: true });
}

export async function teardownExternalLinks(slug: string) {
  const stack = stackToken();
  const failures: unknown[] = [];
  for (const example of examples) {
    const tool = "linkTool" in example ? (example.linkTool as LinkTool) : undefined;
    try {
      if (tool === "sst") await removeSst(example, slug, stack);
      if (tool === "pulumi") await removePulumi(example, slug, stack);
    } catch (error) {
      failures.push(error);
    }
  }
  if (failures.length) {
    throw new AggregateError(failures, "external link teardown failed");
  }
}

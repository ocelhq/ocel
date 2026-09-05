import assert from "node:assert/strict";
import { linkRows } from "../../rows";
import { JOURNEY_CONFIG } from "../../config";
import { ocel, spawnOcel, workTree } from "../../ocel";
import type { LadderRow } from "../../spec";
import type { CellContext } from "../types";
import { place } from "./place";
import { awsLinkStore, awsStore, type Cli, cliAt, said } from "./store";

export const LINK_NAME = "orders";
export const LINK_TYPE = "postgres";
export const CUSTOM_LINK_NAME = "network";
export const CUSTOM_LINK_TYPE = "custom";
export const LINK_NAMES = [LINK_NAME, CUSTOM_LINK_NAME] as const;

const NOTHING_PUBLISHED = "nothing has published a record under that name";
const NOTHING_AT_ALL = "Nothing at all is published to";

const VPC_ACCESS_POLICY_ARN =
  "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole";

export type PublishedPlacement = { subnetIds: string[]; securityGroupIds: string[] };

const placements = new Map<string, PublishedPlacement>();

export function recordPlacement(slug: string, placement: PublishedPlacement): void {
  placements.set(slug, placement);
}

function placementFor(slug: string): PublishedPlacement {
  const found = placements.get(slug);
  if (!found) {
    throw new Error(
      `${slug} carries no recorded placement; a rung's beforeUp hook records the subnet and security group ids its IaC deploy published before consume reads them back`,
    );
  }
  return found;
}

const seenOwners = new Map<string, Set<string>>();

function noteOwner(slug: string, owner: string): void {
  const owners = seenOwners.get(slug) ?? new Set<string>();
  owners.add(owner);
  seenOwners.set(slug, owners);
}

function ownersOf(slug: string): string[] {
  return [...(seenOwners.get(slug) ?? [])];
}

async function endpoint(): Promise<string | undefined> {
  return (await place()).endpoint;
}

async function cli(): Promise<Cli> {
  return cliAt(await endpoint());
}

async function linkStore() {
  return awsLinkStore(await endpoint());
}

async function taggedFunctionArns(slug: string): Promise<string[]> {
  const raw = await (await cli())([
    "resourcegroupstaggingapi",
    "get-resources",
    "--resource-type-filters",
    "lambda:function",
    "--tag-filters",
    `Key=ocel:project,Values=${slug}`,
    "--output",
    "json",
  ]);
  const parsed = JSON.parse(raw) as { ResourceTagMappingList?: Array<{ ResourceARN?: string }> };
  return (parsed.ResourceTagMappingList ?? [])
    .map((row) => row.ResourceARN)
    .filter((arn): arn is string => Boolean(arn));
}

type FunctionConfiguration = {
  Role?: string;
  Environment?: { Variables?: Record<string, string> };
  VpcConfig?: { SubnetIds?: string[]; SecurityGroupIds?: string[] };
};

async function functionConfiguration(functionArn: string): Promise<FunctionConfiguration> {
  const raw = await (await cli())([
    "lambda",
    "get-function-configuration",
    "--function-name",
    functionArn,
    "--output",
    "json",
  ]);
  return JSON.parse(raw) as FunctionConfiguration;
}

type PolicyDocument = { Statement: Array<{ Effect?: string; Action?: string | string[]; Resource?: string | string[] }> };

async function attachedManagedPolicyArns(roleName: string): Promise<string[]> {
  const raw = await (await cli())([
    "iam",
    "list-attached-role-policies",
    "--role-name",
    roleName,
    "--output",
    "json",
  ]);
  const parsed = JSON.parse(raw) as { AttachedPolicies?: Array<{ PolicyArn?: string }> };
  return (parsed.AttachedPolicies ?? [])
    .map((row) => row.PolicyArn)
    .filter((arn): arn is string => Boolean(arn));
}

async function inlinePolicyDocument(roleName: string, policyName: string): Promise<PolicyDocument | undefined> {
  let raw: string;
  try {
    raw = await (await cli())([
      "iam",
      "get-role-policy",
      "--role-name",
      roleName,
      "--policy-name",
      policyName,
      "--query",
      "PolicyDocument",
      "--output",
      "json",
    ]);
  } catch (error) {
    if (/NoSuchEntity/.test(said(error))) {
      return undefined;
    }
    throw error;
  }
  return JSON.parse(raw) as PolicyDocument;
}

function statementsGrant(document: PolicyDocument | undefined, action: string, resource: string): boolean {
  if (!document) {
    return false;
  }
  return document.Statement.some((statement) => {
    if (statement.Effect !== "Allow") {
      return false;
    }
    const actions = Array.isArray(statement.Action) ? statement.Action : [statement.Action];
    const resources = Array.isArray(statement.Resource) ? statement.Resource : [statement.Resource];
    return actions.includes(action) && resources.includes(resource);
  });
}

function roleNameOf(roleArn: string | undefined): string {
  const name = roleArn?.split("/").pop();
  if (!name) {
    throw new Error(`no execution role reported for the tagged function (${roleArn ?? "none"})`);
  }
  return name;
}

export async function refuse(cell: CellContext): Promise<void> {
  const dir = await workTree(cell, "aws");
  const env = {
    ...process.env,
    OCEL_CONFIG: `${dir}/${JOURNEY_CONFIG}`,
  };
  const result = await spawnOcel(dir, ["deploy", "--yes"], env);
  await cell.evidence.write("up", "refuse.stdout", result.stdout);
  await cell.evidence.write("up", "refuse.stderr", result.stderr);
  const output = `${result.stdout}\n${result.stderr}`;

  assert.notEqual(
    result.code,
    0,
    "ocel deploy exited 0 with nothing published; a link is resolved before anything is provisioned",
  );
  assert.equal(
    await awsStore(await endpoint()).stands(cell.slug),
    false,
    `${cell.slug} has a project before anything published a link`,
  );
  assert.deepEqual(
    await taggedFunctionArns(cell.slug),
    [],
    `${cell.slug} carries a tagged function before anything published a link`,
  );
  assert.ok(
    output.includes(LINK_NAME),
    `the refusal does not name the bound link ${LINK_NAME}: ${output}`,
  );
  assert.ok(
    output.includes(NOTHING_PUBLISHED),
    `the refusal does not say why it stopped: ${output}`,
  );
  assert.ok(
    output.includes(NOTHING_AT_ALL),
    `the refusal does not confirm nothing at all is published yet: ${output}`,
  );
}

export const ladderRows: LadderRow[] = [
  {
    title: "ocel link ls lists both records with their name, type, source and owner",
    phase: "publish",
    run: async (cell) => {
      const dir = await workTree(cell, "aws");
      const env = {
        ...process.env,
        OCEL_CONFIG: `${dir}/${JOURNEY_CONFIG}`,
      };
      const result = await ocel(dir, ["link", "ls", "--log-format", "json"], env);
      const parsed = JSON.parse(result.stdout) as {
        links: Array<{ name: string; type: string; source: string; owner: string }>;
      };
      for (const name of LINK_NAMES) {
        const listed = parsed.links.filter((row) => row.name === name);
        assert.equal(listed.length, 1, `ocel link ls lists ${listed.length} records named ${name}, want 1`);
        assert.ok(listed[0]!.type.length > 0, `${name} is listed with no type`);
        assert.ok(listed[0]!.source.length > 0, `${name} is listed with no source`);
        assert.ok(listed[0]!.owner.length > 0, `${name} is listed with no owner`);
      }
    },
  },
  {
    title: "each record is stamped with the publisher's URN and holds nothing beside the sealed value",
    phase: "publish",
    run: async (cell) => {
      const records = await (await linkStore()).records(cell.slug);
      for (const name of LINK_NAMES) {
        const record = records.find((row) => row.name === name);
        assert.ok(record, `no record named ${name} is published`);
        assert.match(record!.owner, /^urn:/, `${name}'s record is owned by ${record!.owner}, not a publisher URN`);
        assert.deepEqual(
          record!.redactedProperties,
          {},
          `${name}'s record carries ${JSON.stringify(record!.redactedProperties)} in the clear`,
        );
        noteOwner(cell.slug, record!.owner);
      }
    },
  },
  {
    title: "the value row beside each record carries ciphertext",
    phase: "publish",
    run: async (cell) => {
      const values = await (await linkStore()).values(cell.slug);
      for (const name of LINK_NAMES) {
        const value = values.find((row) => row.name === name);
        assert.ok(value, `no value row is published for ${name}`);
        assert.ok(value!.sealed.length > 0, `${name}'s value row carries no sealed bytes`);
      }
    },
  },
  {
    title: "grants are scoped to the named resource: orders carries rds-db:connect, network carries none",
    phase: "publish",
    run: async (cell) => {
      const records = await (await linkStore()).records(cell.slug);
      const orders = records.find((row) => row.name === LINK_NAME);
      assert.ok(orders, `no record named ${LINK_NAME} is published`);
      assert.ok(
        orders!.grants.length > 0 &&
          orders!.grants.every((grant) => grant.actions.length > 0 && grant.resources.length > 0),
        `${LINK_NAME}'s grants are not scoped to a resource: ${JSON.stringify(orders!.grants)}`,
      );
      assert.ok(
        orders!.grants.some((grant) => grant.actions.includes("rds-db:connect")),
        `${LINK_NAME} carries no rds-db:connect grant: ${JSON.stringify(orders!.grants)}`,
      );
      const network = records.find((row) => row.name === CUSTOM_LINK_NAME);
      assert.ok(network, `no record named ${CUSTOM_LINK_NAME} is published`);
      assert.deepEqual(
        network!.grants,
        [],
        `${CUSTOM_LINK_NAME} carries grants, and no consumer attaches a custom link's grants`,
      );
    },
  },
  {
    title: "the publisher's index owns exactly its one link",
    phase: "publish",
    run: async (cell) => {
      const records = await (await linkStore()).records(cell.slug);
      for (const name of LINK_NAMES) {
        const record = records.find((row) => row.name === name);
        assert.ok(record, `no record named ${name} is published`);
        const owned = await (await linkStore()).ownerIndex(cell.slug, record!.owner);
        assert.deepEqual(
          owned,
          [name],
          `${record!.owner}'s index carries ${JSON.stringify(owned)}, want exactly [${JSON.stringify(name)}]`,
        );
      }
    },
  },
  {
    title: "ownership is unchanged and ocel's own index claims neither name",
    phase: "consume",
    run: async (cell) => {
      const records = await (await linkStore()).records(cell.slug);
      for (const name of LINK_NAMES) {
        const record = records.find((row) => row.name === name);
        assert.ok(record, `no record named ${name} is published`);
        assert.match(record!.owner, /^urn:/, `${name} is now owned by ${record!.owner}, not the publisher`);
      }
      const ocelIndex = await (await linkStore()).ownerIndex(cell.slug, "OCEL");
      for (const name of LINK_NAMES) {
        assert.ok(
          !(ocelIndex ?? []).includes(name),
          `ocel's own index claims ${name}, and a consumer never becomes a publisher`,
        );
      }
    },
  },
  {
    title: "every tagged function carries the postgres env key with no clear-text host, database or password",
    phase: "consume",
    run: async (cell, live) => {
      assert.ok(live, "consume ran with no live deployment to read the link report from");
      const { body } = await (async () => {
        const res = await live!.fetch(`${live!.baseUrl}/api/link`);
        return { body: (await res.json()) as { host: string; database: string } };
      })();
      const arns = await taggedFunctionArns(cell.slug);
      assert.ok(arns.length > 0, `${cell.slug} carries no tagged function`);
      for (const arn of arns) {
        const configuration = await functionConfiguration(arn);
        const variables = configuration.Environment?.Variables ?? {};
        const key = `OCEL_RESOURCE_POSTGRES_${LINK_NAME}`;
        assert.ok(key in variables, `${arn} carries no ${key}`);
        for (const [envKey, value] of Object.entries(variables)) {
          assert.ok(
            !value.includes(body.host),
            `${arn}'s ${envKey} carries the host in the clear`,
          );
          assert.ok(
            !value.includes(body.database),
            `${arn}'s ${envKey} carries the database in the clear`,
          );
        }
      }
    },
  },
  {
    title: "a VPC config equal to the published ids, and execution roles with the VPC policy and the published grants",
    phase: "consume",
    run: async (cell) => {
      const placement = placementFor(cell.slug);
      const records = await (await linkStore()).records(cell.slug);
      const orders = records.find((row) => row.name === LINK_NAME);
      assert.ok(orders, `no record named ${LINK_NAME} is published`);
      const grant = orders!.grants.find((row) => row.actions.includes("rds-db:connect"));
      assert.ok(grant, `${LINK_NAME} carries no rds-db:connect grant`);

      for (const arn of await taggedFunctionArns(cell.slug)) {
        const configuration = await functionConfiguration(arn);
        assert.deepEqual(
          [...(configuration.VpcConfig?.SubnetIds ?? [])].sort(),
          [...placement.subnetIds].sort(),
          `${arn} runs in subnets ${JSON.stringify(configuration.VpcConfig?.SubnetIds)}, want ${JSON.stringify(placement.subnetIds)}`,
        );
        assert.deepEqual(
          [...(configuration.VpcConfig?.SecurityGroupIds ?? [])].sort(),
          [...placement.securityGroupIds].sort(),
          `${arn} runs in security groups ${JSON.stringify(configuration.VpcConfig?.SecurityGroupIds)}, want ${JSON.stringify(placement.securityGroupIds)}`,
        );

        const roleName = roleNameOf(configuration.Role);
        const managed = await attachedManagedPolicyArns(roleName);
        assert.ok(
          managed.includes(VPC_ACCESS_POLICY_ARN),
          `${roleName} carries ${JSON.stringify(managed)}, none of which is ${VPC_ACCESS_POLICY_ARN}`,
        );
        for (const resource of grant!.resources) {
          const document = await inlinePolicyDocument(roleName, `policy-link-${LINK_NAME}`);
          assert.ok(
            statementsGrant(document, "rds-db:connect", resource),
            `${roleName} carries no inline policy allowing rds-db:connect on ${resource}`,
          );
        }
      }
    },
  },
  {
    title: "both link routes answer",
    phase: "consume",
    run: async (_cell, live) => {
      assert.ok(live, "consume ran with no live deployment to reach the link routes on");
      for (const row of linkRows) {
        await row.run(live!);
      }
    },
  },
  {
    title: "the record survives ocel destroy",
    phase: "outlive",
    run: async (cell) => {
      const records = await (await linkStore()).records(cell.slug);
      for (const name of LINK_NAMES) {
        assert.ok(
          records.some((row) => row.name === name),
          `${name}'s record did not survive ocel destroy, and destroy never touches the publisher`,
        );
      }
    },
  },
  {
    title: "both partitions are empty once the publisher is removed",
    phase: "prune",
    run: async (cell) => {
      const records = await (await linkStore()).records(cell.slug);
      assert.deepEqual(
        records,
        [],
        `the links partition still carries ${JSON.stringify(records.map((row) => row.name))}`,
      );
      for (const owner of ownersOf(cell.slug)) {
        const owned = await (await linkStore()).ownerIndex(cell.slug, owner);
        assert.equal(
          owned,
          undefined,
          `${owner}'s index still carries ${JSON.stringify(owned)} after the publisher was removed`,
        );
      }
    },
  },
];

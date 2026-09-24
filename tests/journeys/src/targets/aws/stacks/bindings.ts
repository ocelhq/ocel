import assert from "node:assert/strict";
import path from "node:path";
import { bindingChecks } from "../../../checks";
import { journeyConfigIn } from "../../../config";
import { ocel, spawnOcel, workTree } from "../../../ocel";
import { progress } from "../../../progress";
import type { CellUnderTest } from "../../../run/cellRun";
import type { ExternalStack, StackChecks } from "../../../stacks";
import { awsBindingStore, awsStore, type Cli, cliAt, said } from "../store";
import type { AwsWorld } from "../world";

const BINDING_NAME = "orders";
const CUSTOM_BINDING_NAME = "network";
const BINDING_NAMES = [BINDING_NAME, CUSTOM_BINDING_NAME] as const;

const NOTHING_PUBLISHED = "nothing has published a record under that name";
const NOTHING_AT_ALL = "Nothing at all is published to";

const VPC_ACCESS_POLICY_ARN =
  "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole";

export type PublishedPlacement = { subnetIds: string[]; securityGroupIds: string[] };

async function taggedFunctionArns(cli: Cli, slug: string): Promise<string[]> {
  const raw = await cli([
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

async function functionConfiguration(
  cli: Cli,
  functionArn: string,
): Promise<FunctionConfiguration> {
  const raw = await cli([
    "lambda",
    "get-function-configuration",
    "--function-name",
    functionArn,
    "--output",
    "json",
  ]);
  return JSON.parse(raw) as FunctionConfiguration;
}

type PolicyDocument = {
  Statement: Array<{ Effect?: string; Action?: string | string[]; Resource?: string | string[] }>;
};

async function attachedManagedPolicyArns(cli: Cli, roleName: string): Promise<string[]> {
  const raw = await cli([
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

async function inlinePolicyDocument(
  cli: Cli,
  roleName: string,
  policyName: string,
): Promise<PolicyDocument | undefined> {
  let raw: string;
  try {
    raw = await cli([
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

function statementsGrant(
  document: PolicyDocument | undefined,
  action: string,
  resource: string,
): boolean {
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

export abstract class AwsStack implements ExternalStack {
  constructor(protected readonly world: Pick<AwsWorld, "endpoint">) {}

  private readonly placements = new Map<string, PublishedPlacement>();
  private readonly owners = new Map<string, Set<string>>();

  readonly checks: StackChecks = {
    afterPublish: [
      {
        title: "ocel bindings ls lists both records with their name, type, source and owner",
        run: async (cell) => {
          const dir = await workTree(cell, "aws");
          const env = {
            ...process.env,
            OCEL_CONFIG: path.join(dir, journeyConfigIn(dir)),
          };
          const result = await ocel(dir, ["bindings", "ls", "--log-format", "json"], env);
          const parsed = JSON.parse(result.stdout) as {
            bindings: Array<{ name: string; type: string; source: string; owner: string }>;
          };
          for (const name of BINDING_NAMES) {
            const listed = parsed.bindings.filter((row) => row.name === name);
            assert.equal(
              listed.length,
              1,
              `ocel bindings ls lists ${listed.length} records named ${name}, want 1`,
            );
            assert.ok(listed[0]!.type.length > 0, `${name} is listed with no type`);
            assert.ok(listed[0]!.source.length > 0, `${name} is listed with no source`);
            assert.ok(listed[0]!.owner.length > 0, `${name} is listed with no owner`);
          }
        },
      },
      {
        title:
          "each record is stamped with the publisher's URN and holds nothing beside the sealed value",
        run: async (cell) => {
          const records = await (await this.bindingStore()).records(cell.slug);
          for (const name of BINDING_NAMES) {
            const record = records.find((row) => row.name === name);
            assert.ok(record, `no record named ${name} is published`);
            assert.match(
              record!.owner,
              /^urn:/,
              `${name}'s record is owned by ${record!.owner}, not a publisher URN`,
            );
            assert.deepEqual(
              record!.redactedProperties,
              {},
              `${name}'s record carries ${JSON.stringify(record!.redactedProperties)} in the clear`,
            );
            this.noteOwner(cell.slug, record!.owner);
          }
        },
      },
      {
        title: "the value row beside each record carries ciphertext",
        run: async (cell) => {
          const values = await (await this.bindingStore()).values(cell.slug);
          for (const name of BINDING_NAMES) {
            const value = values.find((row) => row.name === name);
            assert.ok(value, `no value row is published for ${name}`);
            assert.ok(value!.sealed.length > 0, `${name}'s value row carries no sealed bytes`);
          }
        },
      },
      {
        title:
          "grants are scoped to the named resource: orders carries rds-db:connect, network carries none",
        run: async (cell) => {
          const records = await (await this.bindingStore()).records(cell.slug);
          const orders = records.find((row) => row.name === BINDING_NAME);
          assert.ok(orders, `no record named ${BINDING_NAME} is published`);
          assert.ok(
            orders!.grants.length > 0 &&
              orders!.grants.every(
                (grant) => grant.actions.length > 0 && grant.resources.length > 0,
              ),
            `${BINDING_NAME}'s grants are not scoped to a resource: ${JSON.stringify(orders!.grants)}`,
          );
          assert.ok(
            orders!.grants.some((grant) => grant.actions.includes("rds-db:connect")),
            `${BINDING_NAME} carries no rds-db:connect grant: ${JSON.stringify(orders!.grants)}`,
          );
          const network = records.find((row) => row.name === CUSTOM_BINDING_NAME);
          assert.ok(network, `no record named ${CUSTOM_BINDING_NAME} is published`);
          assert.deepEqual(
            network!.grants,
            [],
            `${CUSTOM_BINDING_NAME} carries grants, and no consumer attaches a custom binding's grants`,
          );
        },
      },
      {
        title: "the publisher's index owns exactly its one binding",
        run: async (cell) => {
          const records = await (await this.bindingStore()).records(cell.slug);
          for (const name of BINDING_NAMES) {
            const record = records.find((row) => row.name === name);
            assert.ok(record, `no record named ${name} is published`);
            const owned = await (await this.bindingStore()).ownerIndex(cell.slug, record!.owner);
            assert.deepEqual(
              owned,
              [name],
              `${record!.owner}'s index carries ${JSON.stringify(owned)}, want exactly [${JSON.stringify(name)}]`,
            );
          }
        },
      },
    ],
    whileServing: [
      {
        title: "ownership is unchanged and ocel's own index claims neither name",
        run: async (cell) => {
          const records = await (await this.bindingStore()).records(cell.slug);
          for (const name of BINDING_NAMES) {
            const record = records.find((row) => row.name === name);
            assert.ok(record, `no record named ${name} is published`);
            assert.match(
              record!.owner,
              /^urn:/,
              `${name} is now owned by ${record!.owner}, not the publisher`,
            );
          }
          const ocelIndex = await (await this.bindingStore()).ownerIndex(cell.slug, "OCEL");
          for (const name of BINDING_NAMES) {
            assert.ok(
              !(ocelIndex ?? []).includes(name),
              `ocel's own index claims ${name}, and a consumer never becomes a publisher`,
            );
          }
        },
      },
      {
        title:
          "every tagged function carries the postgres env key with no clear-text host, database or password",
        run: async (cell, serving) => {
          assert.ok(
            serving,
            "a check while serving ran with no deployment to read the binding report from",
          );
          const { body } = await (async () => {
            const res = await serving!.fetch(`${serving!.baseUrl}/api/binding`);
            return { body: (await res.json()) as { host: string; database: string } };
          })();
          const arns = await taggedFunctionArns(await this.cli(), cell.slug);
          assert.ok(arns.length > 0, `${cell.slug} carries no tagged function`);
          for (const arn of arns) {
            const configuration = await functionConfiguration(await this.cli(), arn);
            const variables = configuration.Environment?.Variables ?? {};
            const key = `OCEL_RESOURCE_POSTGRES_${BINDING_NAME}`;
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
        title:
          "a VPC config equal to the published ids, and execution roles with the VPC policy and the published grants",
        run: async (cell) => {
          const placement = this.placementFor(cell.slug);
          const records = await (await this.bindingStore()).records(cell.slug);
          const orders = records.find((row) => row.name === BINDING_NAME);
          assert.ok(orders, `no record named ${BINDING_NAME} is published`);
          const grant = orders!.grants.find((row) => row.actions.includes("rds-db:connect"));
          assert.ok(grant, `${BINDING_NAME} carries no rds-db:connect grant`);

          for (const arn of await taggedFunctionArns(await this.cli(), cell.slug)) {
            const configuration = await functionConfiguration(await this.cli(), arn);
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
            const managed = await attachedManagedPolicyArns(await this.cli(), roleName);
            assert.ok(
              managed.includes(VPC_ACCESS_POLICY_ARN),
              `${roleName} carries ${JSON.stringify(managed)}, none of which is ${VPC_ACCESS_POLICY_ARN}`,
            );
            for (const resource of grant!.resources) {
              const document = await inlinePolicyDocument(
                await this.cli(),
                roleName,
                `policy-binding-${BINDING_NAME}`,
              );
              assert.ok(
                statementsGrant(document, "rds-db:connect", resource),
                `${roleName} carries no inline policy allowing rds-db:connect on ${resource}`,
              );
            }
          }
        },
      },
      {
        title: "both binding routes answer",
        run: async (_cell, serving) => {
          assert.ok(
            serving,
            "a check while serving ran with no deployment to reach the binding routes on",
          );
          for (const check of bindingChecks) {
            await check.run(serving!);
          }
        },
      },
    ],
    afterOcelDestroy: [
      {
        title: "the record survives ocel destroy",
        run: async (cell) => {
          const records = await (await this.bindingStore()).records(cell.slug);
          for (const name of BINDING_NAMES) {
            assert.ok(
              records.some((row) => row.name === name),
              `${name}'s record did not survive ocel destroy, and destroy never touches the publisher`,
            );
          }
        },
      },
    ],
    afterStackDestroy: [
      {
        title: "both partitions are empty once the publisher is removed",
        run: async (cell) => {
          const records = await (await this.bindingStore()).records(cell.slug);
          assert.deepEqual(
            records,
            [],
            `the bindings partition still carries ${JSON.stringify(records.map((row) => row.name))}`,
          );
          for (const owner of this.ownersOf(cell.slug)) {
            const owned = await (await this.bindingStore()).ownerIndex(cell.slug, owner);
            assert.equal(
              owned,
              undefined,
              `${owner}'s index still carries ${JSON.stringify(owned)} after the publisher was removed`,
            );
          }
        },
      },
    ],
  };

  abstract deploy(cell: CellUnderTest): Promise<void>;

  abstract destroy(cell: CellUnderTest): Promise<void>;

  abstract sweepStale(runId: string): Promise<void>;

  abstract sweepRun(runId: string): Promise<void>;

  async refuse(cell: CellUnderTest): Promise<void> {
    const dir = await workTree(cell, "aws");
    const env = {
      ...process.env,
      OCEL_CONFIG: path.join(dir, journeyConfigIn(dir)),
    };
    const result = await spawnOcel(
      dir,
      ["deploy", "--yes"],
      env,
      progress(`${cell.name} deploy/refuse |`),
    );
    await cell.evidence.write("deploy", "refuse.stdout", result.stdout);
    await cell.evidence.write("deploy", "refuse.stderr", result.stderr);
    const output = `${result.stdout}\n${result.stderr}`;

    assert.notEqual(
      result.code,
      0,
      "ocel deploy exited 0 with nothing published; a binding is resolved before anything is provisioned",
    );
    assert.equal(
      await awsStore(await this.world.endpoint()).exists(cell.slug),
      false,
      `${cell.slug} has a project before anything published a binding`,
    );
    assert.deepEqual(
      await taggedFunctionArns(await this.cli(), cell.slug),
      [],
      `${cell.slug} carries a tagged function before anything published a binding`,
    );
    assert.ok(
      output.includes(BINDING_NAME),
      `the refusal does not name the bound binding ${BINDING_NAME}: ${output}`,
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

  private async cli(): Promise<Cli> {
    return cliAt(await this.world.endpoint());
  }

  private async bindingStore() {
    return awsBindingStore(await this.world.endpoint());
  }

  protected recordPlacement(slug: string, placement: PublishedPlacement): void {
    this.placements.set(slug, placement);
  }

  private placementFor(slug: string): PublishedPlacement {
    const found = this.placements.get(slug);
    if (!found) {
      throw new Error(
        `${slug} carries no recorded placement; the stack's deploy records the subnet and security group ids it published before a check while serving reads them back`,
      );
    }
    return found;
  }

  private noteOwner(slug: string, owner: string): void {
    const owners = this.owners.get(slug) ?? new Set<string>();
    owners.add(owner);
    this.owners.set(slug, owners);
  }

  private ownersOf(slug: string): string[] {
    return [...(this.owners.get(slug) ?? [])];
  }
}

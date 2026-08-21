export const linkName = "orders";
export const customLinkName = "network";
export const linkClass = "preview";

export type LinkSource = "sst" | "pulumi";

export type Attribute = {
  S?: string;
  N?: string;
  B?: string;
  SS?: string[];
};

export type StoreRow = Record<string, Attribute>;

export type Grant = {
  actions?: string[];
  resources?: string[];
};

export type PublishedLink = {
  name?: string;
  source?: string;
  postgres?: Record<string, unknown>;
  custom?: Record<string, unknown>;
  grants?: Grant[];
};

export type LinkOutputs = {
  host: string;
  port: string;
  database: string;
  subnetIds: string[];
  securityGroupIds: string[];
};

export type LinkExpectation = {
  name: string;
  type: "postgres" | "custom";
  source: LinkSource;
  owner: string;
};

export function linkOwner(
  source: LinkSource,
  project: string,
  stack: string,
  name = linkName,
) {
  const type =
    source === "sst"
      ? "pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource"
      : "pulumi-nodejs:dynamic:Resource";
  return `urn:pulumi:${stack}::${project}::${type}::ocel-link-${name}`;
}

export function linkPartitionKey(slug: string, name: string) {
  return `PROJECT#${slug}#CLASS#${linkClass}#LINK#${name}`;
}

export function recordSortKey(environment: string) {
  return `RECORD#ENV#${environment}`;
}

export function valueSortKey(environment: string) {
  return `VALUE#FOLDER#/#NAME#PROPERTIES#ENV#${environment}`;
}

export function linkIndexSortKey(owner: string, environment: string) {
  return `LINKS#OWNER#${owner}#ENV#${environment}`;
}

export function pairProblem(record?: StoreRow, value?: StoreRow) {
  if (!record && !value) return "neither row of the pair is present";
  if (!record) return "the value row is published without the record row";
  if (!value) return "the record row is published without the value row";
  const versions = [Number(record.version?.N ?? 0), Number(value.version?.N ?? 0)];
  if (versions[0] !== versions[1]) {
    return `the pair carries versions ${versions[0]} and ${versions[1]}`;
  }
  if (!value.ciphertext?.B) return "the value row carries no ciphertext";
  return null;
}

export function publishedRecordProblem(
  row: StoreRow | undefined,
  expected: LinkExpectation,
) {
  if (!row?.record?.S) return "the record row carries no record attribute";
  if (row.owner?.S !== expected.owner) {
    return `the record row is stamped ${row.owner?.S ?? "with no owner"}, want ${expected.owner}`;
  }
  let record: PublishedLink;
  try {
    record = JSON.parse(row.record.S) as PublishedLink;
  } catch (error) {
    return `the record row is not parseable JSON: ${(error as Error).message}`;
  }
  if (record.name !== expected.name) {
    return `the record is published as ${JSON.stringify(record.name ?? null)}, want ${JSON.stringify(expected.name)}`;
  }
  if (record.source !== expected.source) {
    return `the record is sourced ${JSON.stringify(record.source ?? null)}, want ${JSON.stringify(expected.source)}`;
  }
  const properties = record[expected.type];
  if (!properties) return `the record carries no ${expected.type} properties`;
  const clear = Object.keys(properties);
  if (clear.length > 0) {
    return `the record row carries ${clear.join(", ")} in the clear`;
  }
  if (expected.type === "custom") {
    return (record.grants ?? []).length === 0
      ? null
      : "the custom record carries grants";
  }
  const grant = grantProblem(record.grants);
  if (grant) return grant;
  return (record.grants ?? []).some((entry) =>
    (entry.actions ?? []).includes("rds-db:connect"),
  )
    ? null
    : "the postgres record grants no rds-db:connect";
}

export function listedLinkProblem(
  links: Array<{
    name?: string;
    type?: string;
    source?: string;
    owner?: string;
  }>,
  expected: LinkExpectation,
) {
  const listed = links.filter((entry) => entry.name === expected.name);
  if (listed.length !== 1) {
    return `link ls lists ${listed.length} records named ${expected.name}`;
  }
  const entry = listed[0]!;
  if (entry.type !== expected.type) {
    return `${expected.name} is listed as ${entry.type}, want ${expected.type}`;
  }
  if (entry.source !== expected.source) {
    return `${expected.name} is listed with source ${entry.source}, want ${expected.source}`;
  }
  if (entry.owner !== expected.owner) {
    return `${expected.name} is listed as owned by ${entry.owner}, want ${expected.owner}`;
  }
  return null;
}

export function grantProblem(grants?: Grant[]) {
  for (const grant of grants ?? []) {
    const actions = grant.actions ?? [];
    const resources = grant.resources ?? [];
    if (
      actions.length === 0 ||
      actions.some((action) => {
        const parts = action.split(":");
        return parts.length !== 2 || parts.includes("*");
      })
    ) {
      return `grant over ${actions.join(", ") || "no action"} names a whole service`;
    }
    if (resources.length === 0 || resources.includes("*")) {
      return `grant over ${resources.join(", ") || "no resource"} reaches past the link`;
    }
  }
  return null;
}

function list(value: unknown): string[] {
  if (typeof value === "string") return [value];
  return Array.isArray(value) ? value.map(String) : [];
}

function statements(documents: Array<Record<string, unknown>>) {
  return documents.flatMap((document) => {
    const value = document.Statement;
    if (!value) return [];
    return Array.isArray(value) ? value : [value];
  }) as Array<Record<string, unknown>>;
}

export function grantsDeliveredProblem(
  documents: Array<Record<string, unknown>>,
  grants?: Grant[],
) {
  if (!grants?.length) return "the record grants nothing";
  const policies = statements(documents);
  for (const grant of grants) {
    for (const action of grant.actions ?? []) {
      for (const resource of grant.resources ?? []) {
        const delivered = policies.some(
          (statement) =>
            statement.Effect === "Allow" &&
            list(statement.Action).includes(action) &&
            list(statement.Resource).includes(resource),
        );
        if (!delivered) return `no inline policy allows ${action} on ${resource}`;
      }
    }
  }
  return null;
}

export function varsReachProblem(
  documents: Array<Record<string, unknown>>,
  partitionKey: string,
) {
  return statements(documents).some(
    (statement) =>
      statement.Effect === "Allow" &&
      list(statement.Action).includes("dynamodb:Query") &&
      contains(statement.Condition, partitionKey),
  )
    ? null
    : `no inline policy reaches ${partitionKey}`;
}

function contains(value: unknown, expected: string): boolean {
  if (value === expected) return true;
  if (Array.isArray(value)) {
    return value.some((entry) => contains(entry, expected));
  }
  if (value && typeof value === "object") {
    return Object.values(value).some((entry) => contains(entry, expected));
  }
  return false;
}

export function vpcPlacementProblem(
  configuration: {
    VpcConfig?: { SubnetIds?: string[]; SecurityGroupIds?: string[] };
  },
  outputs: LinkOutputs,
) {
  const placed = configuration.VpcConfig ?? {};
  for (const [name, expected, actual] of [
    ["subnets", outputs.subnetIds, placed.SubnetIds ?? []],
    [
      "security groups",
      outputs.securityGroupIds,
      placed.SecurityGroupIds ?? [],
    ],
  ] as const) {
    if (
      JSON.stringify([...expected].sort()) !==
      JSON.stringify([...actual].sort())
    ) {
      return `the function runs in ${name} ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`;
    }
  }
  return null;
}

export function refusalProblem(status: number | null, output: string) {
  if (status === 0) return "the deploy succeeded without the network link";
  const missing = [
    output.includes(`"${customLinkName}"`) ? null : customLinkName,
    output.includes("vpc.subnetIds") || output.includes("vpc.securityGroupIds")
      ? null
      : "the transform field",
    output.includes("nothing has published a record under that name")
      ? null
      : "the refusal reason",
  ].filter(Boolean);
  return missing.length ? `the refusal does not name ${missing.join(", ")}` : null;
}

export function linkRecord(row?: StoreRow): PublishedLink | undefined {
  if (!row?.record?.S) return undefined;
  return JSON.parse(row.record.S) as PublishedLink;
}

export function splitIds(value: unknown) {
  if (Array.isArray(value)) return value.map(String).filter(Boolean);
  return String(value ?? "")
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
}

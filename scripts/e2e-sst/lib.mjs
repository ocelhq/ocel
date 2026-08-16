export const SLUG_PREFIX = "e2es";

export const STATE_FILE = ".ocel-e2e-sst.json";

export const CONSUMER_STATE_FILE = ".ocel-e2e-sst-consumer.json";

export const CONSUMER_APP = "orders";

export const DEPLOY_RESULT_FILE = ".ocel/deploy-result.json";

export const LINK_NAME = "orders";

export const LINK_TYPE = "postgres";

export const LINK_SOURCE = "sst";

export const CLASS = "production";

export const LOG_PREFIX = "[ocel-e2e-sst]";

export const DYNAMIC_RESOURCE_TYPE = "pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource";

export function logicalName(link = LINK_NAME) {
  return `ocel-link-${link}`;
}

export function linkOwner({ app, stage, link = LINK_NAME }) {
  return [
    `urn:pulumi:${stage}`,
    app,
    DYNAMIC_RESOURCE_TYPE,
    logicalName(link),
  ].join("::");
}

export function projectSlugForRun(runId = process.env.GITHUB_RUN_ID) {
  const sanitized = String(runId ?? "").replace(/[^a-z0-9]/gi, "").toLowerCase();
  return sanitized ? `${SLUG_PREFIX}-${sanitized}` : `${SLUG_PREFIX}-local`;
}

export function linkPartitionKey(slug, klass, link) {
  return `PROJECT#${slug}#CLASS#${klass}#LINK#${link}`;
}

export function recordSortKey(environment) {
  return `RECORD#ENV#${environment || "*"}`;
}

export function valueSortKey(environment) {
  return `VALUE#FOLDER#/#NAME#PROPERTIES#ENV#${environment || "*"}`;
}

export function linkIndexSortKey(owner, environment) {
  return `LINKS#OWNER#${owner}#ENV#${environment || "*"}`;
}

export function linkEnvKey(link = LINK_NAME) {
  return `OCEL_RESOURCE_POSTGRES_${link}`;
}

export function renderSstConfig({ app, projectDir, region, link = LINK_NAME }) {
  return `/// <reference path="./.sst/platform/config.d.ts" />

export default $config({
  app() {
    return { name: ${JSON.stringify(app)}, home: "aws", providers: { aws: { region: ${JSON.stringify(region)} } } };
  },
  async run() {
    const { link } = await import("@ocel/sst");
    const vpc = new sst.aws.Vpc("Vpc");
    const orders = new sst.aws.Postgres("Orders", { vpc });
    const account = aws.getCallerIdentityOutput().accountId;
    link.postgres(
      ${JSON.stringify(link)},
      {
        host: orders.host,
        port: orders.port,
        database: orders.database,
        username: orders.username,
        password: orders.password,
        grants: [
          {
            label: "connect",
            actions: ["rds-db:connect"],
            resources: [
              $interpolate\`arn:aws:rds-db:${region}:\${account}:dbuser:\${orders.nodes.cluster.clusterResourceId}/\${orders.username}\`,
            ],
          },
        ],
      },
      { class: ${JSON.stringify(CLASS)}, project: ${JSON.stringify(projectDir)} },
    );
    return { host: orders.host, database: orders.database, port: orders.port };
  },
});
`;
}

export function renderOcelConfig({ slug, app = CONSUMER_APP, link = LINK_NAME }) {
  return `import { defineConfig } from "ocel/config";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: ${JSON.stringify(slug)},
  provider: awsProvider(),
  links: [${JSON.stringify(link)}],
  apps: [{ name: ${JSON.stringify(app)}, framework: "express", path: "." }],
});
`;
}

const PROPERTY_KINDS = ["postgres", "bucket"];

export function linkTypeOf(record) {
  return PROPERTY_KINDS.find((kind) => record?.[kind] !== undefined) ?? null;
}

export function parsePublishedRecord(row) {
  if (!row || !row.record || typeof row.record.S !== "string") {
    return { problem: "the record row carries no record attribute" };
  }
  let record;
  try {
    record = JSON.parse(row.record.S);
  } catch (err) {
    return { problem: `the record row is not parseable JSON: ${err.message}` };
  }
  if (!record.name) {
    return { problem: "the record carries no name, which is what a consuming app binds to" };
  }
  const type = linkTypeOf(record);
  if (!type) {
    return { problem: "the record carries no properties, so it names no type a consumer can resolve it against" };
  }
  return { record, type, version: Number(row.version?.N ?? 0) };
}

export function recordShapeProblem(record, { name = LINK_NAME, type = LINK_TYPE, source = LINK_SOURCE } = {}) {
  if (record?.name !== name) {
    return `the record is published as ${JSON.stringify(record?.name ?? null)}, want ${JSON.stringify(name)}`;
  }
  const published = linkTypeOf(record);
  if (published !== type) {
    return `the record carries ${published ?? "no"} properties, want ${type}`;
  }
  if (record.source !== source) {
    return `the record is sourced ${JSON.stringify(record.source ?? null)}, want ${JSON.stringify(source)}; an empty source names what ocel's own provisioning produces`;
  }
  return null;
}

export function redactionProblem(record) {
  const type = linkTypeOf(record);
  if (!type) {
    return "the record carries no properties, so nothing proves the store redacted them";
  }
  const kept = Object.keys(record[type] ?? {});
  if (kept.length > 0) {
    return `the record row carries ${kept.join(", ")} in the clear; the record beside the sealed value holds no property`;
  }
  return null;
}

export function listedLinkProblem(links, { name = LINK_NAME, type = LINK_TYPE, source = LINK_SOURCE, owner }) {
  const listed = (links ?? []).filter((entry) => entry.name === name);
  if (listed.length !== 1) {
    return `\`ocel link ls\` lists ${JSON.stringify((links ?? []).map((entry) => entry.name))}, want exactly one ${name}`;
  }
  const [entry] = listed;
  if (entry.type !== type) {
    return `${name} is listed as ${entry.type}, want ${type}`;
  }
  if (entry.source !== source) {
    return `${name} is listed with source ${JSON.stringify(entry.source ?? null)}, want ${JSON.stringify(source)}`;
  }
  if (entry.owner !== owner) {
    return `${name} is listed as owned by ${entry.owner}, want ${owner}`;
  }
  return null;
}

export function unscopedAction(action) {
  const separator = action.indexOf(":");
  if (separator < 0) return action === "*";
  return action.slice(0, separator) === "*" || action.slice(separator + 1) === "*";
}

export function grantProblem(grants) {
  for (const grant of grants ?? []) {
    const actions = grant.actions ?? [];
    const resources = grant.resources ?? [];
    if (actions.length === 0 || actions.some(unscopedAction)) {
      return `grant over ${actions.join(", ") || "no action"} names a whole service`;
    }
    if (resources.length === 0 || resources.includes("*")) {
      return `grant over ${resources.join(", ") || "no resource"} reaches past the link`;
    }
  }
  return null;
}

export function pairProblem({ record, value }) {
  if (!record && !value) return "neither row of the pair is present";
  if (!record) return "the value row is published without the record row beside it";
  if (!value) return "the record row is published without the value row beside it";
  const versions = [Number(record.version?.N ?? 0), Number(value.version?.N ?? 0)];
  if (versions[0] !== versions[1]) {
    return `the pair carries versions ${versions[0]} and ${versions[1]}; a consumer would read half of one publish`;
  }
  return null;
}

export function parseSstOutputs(stdout) {
  const outputs = {};
  for (const line of String(stdout ?? "").split("\n")) {
    const match = /^\s*([A-Za-z][A-Za-z0-9_]*)\s*:\s*(\S.*?)\s*$/.exec(line);
    if (match) {
      outputs[match[1]] = match[2];
    }
  }
  return outputs;
}

export function ownerProblem(row, owner) {
  const stamped = row?.owner?.S;
  if (!stamped) {
    return "the record row carries no owner stamp, so any publisher's prune would take it";
  }
  return stamped === owner ? null : `the record row is stamped ${stamped}, want ${owner}`;
}

export function deliveredEnvProblem(env, link = LINK_NAME) {
  const key = linkEnvKey(link);
  if (!(key in (env ?? {}))) {
    return `the function's environment carries no ${key}; the app reads the resource under the name it declared, whoever provisioned it`;
  }
  return null;
}

export function credentialLeakProblem(env, secrets) {
  for (const secret of secrets ?? []) {
    if (!secret) continue;
    for (const [key, value] of Object.entries(env ?? {})) {
      if (String(value).includes(secret)) {
        return `${key} carries the published credential in the clear`;
      }
    }
  }
  return null;
}

function statementsOf(documents) {
  return (documents ?? []).flatMap((doc) => {
    const statement = doc?.Statement ?? [];
    return Array.isArray(statement) ? statement : [statement];
  });
}

function listOf(value) {
  if (value === undefined || value === null) return [];
  return Array.isArray(value) ? value : [value];
}

export function grantsDeliveredProblem(documents, grants) {
  if (!grants || grants.length === 0) {
    return "the record grants nothing, so the deploy had no permission to render and this proves nothing";
  }
  const statements = statementsOf(documents);
  for (const grant of grants) {
    for (const action of grant.actions ?? []) {
      for (const resource of grant.resources ?? []) {
        const granted = statements.some(
          (s) =>
            s.Effect === "Allow" &&
            listOf(s.Action).includes(action) &&
            listOf(s.Resource).includes(resource),
        );
        if (!granted) {
          return `no inline policy allows ${action} on ${resource}, which the published record grants`;
        }
      }
    }
  }
  return null;
}

export function varsReachProblem(documents, partitionKey) {
  const reaches = statementsOf(documents).some(
    (s) => s.Effect === "Allow" && listOf(s.Resource).some((r) => String(r).includes(partitionKey)),
  );
  return reaches
    ? null
    : `no inline policy reaches ${partitionKey}, so the app cannot read the link's values at cold start`;
}

export function resolvedProblem(reported, expected) {
  if (!reported || typeof reported !== "object") {
    return "the app served no link report, so nothing proves it resolved the published record";
  }
  for (const [key, want] of Object.entries(expected)) {
    if (String(reported[key] ?? "") !== String(want)) {
      return `the app resolved ${key}=${JSON.stringify(reported[key] ?? null)}, want ${JSON.stringify(String(want))}`;
    }
  }
  return null;
}

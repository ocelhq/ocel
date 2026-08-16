import { describe, expect, it } from "vitest";
import {
  LINK_NAME,
  PUBLISHER,
  credentialLeakProblem,
  deliveredEnvProblem,
  grantProblem,
  grantsDeliveredProblem,
  linkEnvKey,
  linkIndexSortKey,
  linkPartitionKey,
  namesAbsentSubstrate,
  ownerProblem,
  pairProblem,
  parsePublishedRecord,
  parseSstOutputs,
  projectSlugForRun,
  recordSortKey,
  renderOcelConfig,
  renderSstConfig,
  resolvedProblem,
  valueSortKey,
  varsReachProblem,
} from "./lib.mjs";

describe("projectSlugForRun", () => {
  it("namespaces this suite's projects apart from every other suite's", () => {
    expect(projectSlugForRun("12345")).toBe("e2es-12345");
    expect(projectSlugForRun(undefined)).toBe("e2es-local");
    expect(projectSlugForRun("ab/CD-9")).toBe("e2es-abcd9");
  });
});

describe("store keys", () => {
  it("address the per-link partition the publisher writes into", () => {
    expect(linkPartitionKey("e2es-1", "production", "orders")).toBe(
      "PROJECT#e2es-1#CLASS#production#LINK#orders",
    );
    expect(recordSortKey("")).toBe("RECORD#ENV#*");
    expect(valueSortKey("")).toBe("VALUE#FOLDER#/#NAME#PROPERTIES#ENV#*");
    expect(linkIndexSortKey(PUBLISHER, "")).toBe("LINKS#OWNER#sst:OcelLinks#ENV#*");
    expect(linkIndexSortKey(PUBLISHER, "pr-9")).toBe("LINKS#OWNER#sst:OcelLinks#ENV#pr-9");
    expect(linkIndexSortKey("OCEL", "")).toBe("LINKS#OWNER#OCEL#ENV#*");
    expect(linkEnvKey("orders")).toBe("OCEL_RESOURCE_POSTGRES_orders");
  });
});

describe("ownerProblem", () => {
  it("passes a row the publisher stamped and names one it did not", () => {
    expect(ownerProblem({ owner: { S: PUBLISHER } }, PUBLISHER)).toBeNull();
    expect(ownerProblem({ owner: { S: "OCEL" } }, PUBLISHER)).toMatch(/stamped OCEL/);
    expect(ownerProblem({}, PUBLISHER)).toMatch(/no owner stamp/);
  });
});

describe("renderSstConfig", () => {
  it("publishes as a side effect of the user's own run, wrapping nothing", () => {
    const config = renderSstConfig({ app: "e2es", project: "e2es-1", region: "us-east-1" });
    expect(config).toContain('new sst.aws.Postgres("Orders"');
    expect(config).toContain('publish("OcelLinks"');
    expect(config).toContain('project: "e2es-1"');
    expect(config).not.toContain("ocel deploy");
    expect(config).not.toContain("$app.stage");
  });

  it("publishes the component itself, never a resource it is built out of", () => {
    const config = renderSstConfig({ app: "e2es", project: "e2es-1", region: "us-east-1" });
    expect(config).toContain("urn: orders.urn");
    expect(config).not.toContain("nodes.cluster.urn");
  });

  it("carries the property an ocel-declared postgres is read through, and a scoped grant", () => {
    const config = renderSstConfig({ app: "e2es", project: "e2es-1", region: "us-east-1" });
    expect(config).toContain("connectionString:");
    expect(config).toContain('actions: ["rds-db:connect"]');
    expect(config).not.toContain('resources: ["*"]');
  });
});

describe("renderOcelConfig", () => {
  it("binds the published name through `links` and declares it as a resource", () => {
    const config = renderOcelConfig({ slug: "e2es-1" });
    expect(config).toContain('slug: "e2es-1"');
    expect(config).toContain(`links: ["${LINK_NAME}"]`);
    expect(config).not.toContain("mode:");
  });
});

describe("parseSstOutputs", () => {
  it("reads the outputs the config returns and ignores SST's own chatter", () => {
    const outputs = parseSstOutputs(
      ["", "   host: orders.cluster-abc.us-east-1.rds.amazonaws.com", "   port: 5432", "   database: orders", "---"].join("\n"),
    );
    expect(outputs.host).toBe("orders.cluster-abc.us-east-1.rds.amazonaws.com");
    expect(outputs.port).toBe("5432");
    expect(outputs.database).toBe("orders");
    expect(parseSstOutputs("").host).toBeUndefined();
  });
});

describe("deliveredEnvProblem", () => {
  it("wants the declared key, whoever provisioned the resource", () => {
    expect(deliveredEnvProblem({ OCEL_RESOURCE_POSTGRES_orders: "x" }, "orders")).toBeNull();
    expect(deliveredEnvProblem({ OCEL_RESOURCE_POSTGRES_other: "x" }, "orders")).toMatch(
      /OCEL_RESOURCE_POSTGRES_orders/,
    );
  });
});

describe("credentialLeakProblem", () => {
  it("names an env that carries a published value in the clear", () => {
    expect(credentialLeakProblem({ A: "opaque" }, ["secret-host"])).toBeNull();
    expect(credentialLeakProblem({ A: "postgres://secret-host/x" }, ["secret-host"])).toMatch(/A/);
    expect(credentialLeakProblem({ A: "x" }, [undefined, ""])).toBeNull();
  });
});

describe("grantsDeliveredProblem", () => {
  const grants = [{ actions: ["rds-db:connect"], resources: ["arn:aws:rds-db:::dbuser:c/app"] }];

  it("passes when an inline policy allows exactly what the record grants", () => {
    expect(
      grantsDeliveredProblem(
        [
          {
            Statement: [
              { Effect: "Allow", Action: ["rds-db:connect"], Resource: ["arn:aws:rds-db:::dbuser:c/app"] },
            ],
          },
        ],
        grants,
      ),
    ).toBeNull();
  });

  it("names a grant the deploy did not render", () => {
    expect(grantsDeliveredProblem([], grants)).toMatch(/rds-db:connect/);
    expect(
      grantsDeliveredProblem(
        [{ Statement: { Effect: "Allow", Action: "rds-db:connect", Resource: "arn:other" } }],
        grants,
      ),
    ).toMatch(/rds-db:connect/);
  });

  it("accepts a single-valued statement, which IAM renders unwrapped", () => {
    expect(
      grantsDeliveredProblem(
        [
          {
            Statement: {
              Effect: "Allow",
              Action: "rds-db:connect",
              Resource: "arn:aws:rds-db:::dbuser:c/app",
            },
          },
        ],
        grants,
      ),
    ).toBeNull();
  });
});

describe("varsReachProblem", () => {
  const pk = "PROJECT#e2es-1#CLASS#production#LINK#orders";

  it("passes a role that reaches the link's own partition", () => {
    expect(
      varsReachProblem(
        [{ Statement: [{ Effect: "Allow", Action: "dynamodb:Query", Resource: `arn:aws:dynamodb:::table/t` }] }],
        pk,
      ),
    ).toMatch(/cannot read/);
    expect(
      varsReachProblem(
        [
          {
            Statement: [
              {
                Effect: "Allow",
                Action: "dynamodb:Query",
                Resource: "arn:aws:dynamodb:::table/t",
                Condition: {},
              },
              { Effect: "Allow", Action: "dynamodb:Query", Resource: [`arn:t/${pk}`] },
            ],
          },
        ],
        pk,
      ),
    ).toBeNull();
  });
});

describe("resolvedProblem", () => {
  it("compares what the running app resolved against what was published", () => {
    expect(resolvedProblem({ host: "h", port: "5432" }, { host: "h", port: 5432 })).toBeNull();
    expect(resolvedProblem({ host: "other" }, { host: "h" })).toMatch(/host/);
    expect(resolvedProblem(null, { host: "h" })).toMatch(/no link report/);
  });
});

describe("parsePublishedRecord", () => {
  it("reads the type and grants a publisher wrote", () => {
    const parsed = parsePublishedRecord({
      record: {
        S: JSON.stringify({
          type: "sst:aws.Postgres",
          grants: [{ actions: ["rds-db:connect"], resources: ["arn:x"] }],
        }),
      },
      version: { N: "1" },
    });
    expect(parsed.record.type).toBe("sst:aws.Postgres");
    expect(parsed.version).toBe(1);
  });

  it("names an unparseable bag rather than reporting a pass", () => {
    expect(parsePublishedRecord({ record: { S: "{" } }).problem).toMatch(/parseable/);
    expect(parsePublishedRecord({}).problem).toMatch(/no record/);
    expect(parsePublishedRecord({ record: { S: "{}" } }).problem).toMatch(/type/);
  });
});

describe("grantProblem", () => {
  it("passes a grant scoped to the resource the link names", () => {
    expect(
      grantProblem([{ actions: ["rds-db:connect"], resources: ["arn:aws:rds-db:::x"] }]),
    ).toBeNull();
  });

  it("names an unscoped grant", () => {
    expect(grantProblem([{ actions: ["rds-db:*"], resources: ["arn:x"] }])).toMatch(/service/);
    expect(grantProblem([{ actions: ["rds-db:connect"], resources: ["*"] }])).toMatch(/past/);
    expect(grantProblem([{ actions: [], resources: ["arn:x"] }])).toMatch(/no action/);
  });
});

describe("pairProblem", () => {
  it("passes a pair published together", () => {
    expect(pairProblem({ record: { version: { N: "3" } }, value: { version: { N: "3" } } })).toBeNull();
  });

  it("names a torn pair and a half-published one", () => {
    expect(pairProblem({ record: { version: { N: "3" } }, value: { version: { N: "2" } } })).toMatch(/half/);
    expect(pairProblem({ record: { version: { N: "3" } } })).toMatch(/without the value/);
    expect(pairProblem({ value: { version: { N: "3" } } })).toMatch(/without the record/);
    expect(pairProblem({})).toMatch(/neither/);
  });
});

describe("namesAbsentSubstrate", () => {
  it("recognises the publisher's own refusal, not any failure", () => {
    expect(
      namesAbsentSubstrate(
        "ocel aws provider: this AWS account holds no ocel production substrate, so the links this stack publishes have nowhere to land. Run `ocel bootstrap` against this account",
      ),
    ).toBe(true);
    expect(namesAbsentSubstrate("AccessDenied")).toBe(false);
    expect(namesAbsentSubstrate("")).toBe(false);
  });
});

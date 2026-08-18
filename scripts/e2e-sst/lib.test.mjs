import { describe, expect, it } from "vitest";
import {
  CUSTOM_LINK_NAME,
  CUSTOM_LINK_TYPE,
  LINK_NAME,
  TRANSFORM_MODULE,
  credentialLeakProblem,
  deliveredEnvProblem,
  grantProblem,
  grantsDeliveredProblem,
  linkEnvKey,
  linkIndexSortKey,
  linkOwner,
  linkPartitionKey,
  listedLinkProblem,
  ownerProblem,
  pairProblem,
  parsePublishedRecord,
  parseSstOutputs,
  projectSlugForRun,
  publishedIds,
  recordShapeProblem,
  recordSortKey,
  redactionProblem,
  refusalProblem,
  renderOcelConfig,
  renderSstConfig,
  resolvedProblem,
  valueSortKey,
  varsReachProblem,
  vpcAccessProblem,
  vpcPlacementProblem,
} from "./lib.mjs";

const OWNER = linkOwner({ app: "e2es-1", stage: "e2e" });

describe("projectSlugForRun", () => {
  it("namespaces this suite's projects apart from every other suite's", () => {
    expect(projectSlugForRun("12345")).toBe("e2es-12345");
    expect(projectSlugForRun(undefined)).toBe("e2es-local");
    expect(projectSlugForRun("ab/CD-9")).toBe("e2es-abcd9");
  });
});

describe("linkOwner", () => {
  it("is the URN of the one dynamic resource the link call declares", () => {
    expect(OWNER).toBe(
      "urn:pulumi:e2e::e2es-1::pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource::ocel-link-orders",
    );
    expect(linkOwner({ app: "a", stage: "s", link: "other" })).toMatch(/::ocel-link-other$/);
  });

  it("carries no key delimiter, so it addresses one index row of its own", () => {
    expect(OWNER).not.toContain("#");
  });
});

describe("store keys", () => {
  it("address the per-link partition the publish writes into", () => {
    expect(linkPartitionKey("e2es-1", "production", "orders")).toBe(
      "PROJECT#e2es-1#CLASS#production#LINK#orders",
    );
    expect(recordSortKey("")).toBe("RECORD#ENV#*");
    expect(valueSortKey("")).toBe("VALUE#FOLDER#/#NAME#PROPERTIES#ENV#*");
    expect(linkIndexSortKey(OWNER, "")).toBe(`LINKS#OWNER#${OWNER}#ENV#*`);
    expect(linkIndexSortKey(OWNER, "pr-9")).toBe(`LINKS#OWNER#${OWNER}#ENV#pr-9`);
    expect(linkIndexSortKey("OCEL", "")).toBe("LINKS#OWNER#OCEL#ENV#*");
    expect(linkEnvKey("orders")).toBe("OCEL_RESOURCE_POSTGRES_orders");
  });
});

describe("ownerProblem", () => {
  it("passes a row the link resource stamped and names one it did not", () => {
    expect(ownerProblem({ owner: { S: OWNER } }, OWNER)).toBeNull();
    expect(ownerProblem({ owner: { S: "OCEL" } }, OWNER)).toMatch(/stamped OCEL/);
    expect(ownerProblem({}, OWNER)).toMatch(/no owner stamp/);
  });
});

describe("renderSstConfig", () => {
  const config = renderSstConfig({
    app: "e2es-1",
    projectDir: "/tmp/ocel-e2e-sst-consumer-x",
    region: "us-east-1",
  });

  it("links as a side effect of the user's own run, wrapping nothing", () => {
    expect(config).toContain('new sst.aws.Postgres("Orders"');
    expect(config).toContain('const { link } = await import("@ocel/sst")');
    expect(config).toContain('link.postgres(\n      "orders"');
    expect(config).not.toContain("ocel deploy");
    expect(config).not.toContain("$app.stage");
  });

  it("names the ocel project directory the CLI publishes into, never a slug", () => {
    expect(config).toContain('project: "/tmp/ocel-e2e-sst-consumer-x"');
    expect(config).toContain('class: "production"');
    expect(config).not.toContain("region:\n");
    expect(config).not.toContain("publish(");
    expect(config).not.toContain("urn:");
  });

  it("carries every property a postgres link is, and a scoped grant", () => {
    for (const field of ["host", "port", "database", "username", "password"]) {
      expect(config).toContain(`${field}: orders.${field}`);
    }
    expect(config).toContain('actions: ["rds-db:connect"]');
    expect(config).not.toContain('resources: ["*"]');
  });

  it("returns the outputs the consume leg checks what the app resolved and where it runs against", () => {
    expect(config).toContain("host: orders.host,");
    expect(config).toContain("subnetIds: vpc.privateSubnets.apply((ids) => ids.join(\",\")),");
    expect(config).toContain("securityGroupIds: vpc.securityGroups.apply((ids) => ids.join(\",\")),");
  });

  it("gives the private subnets a route to every service a placed function reaches", () => {
    expect(config).toContain('serviceName: `com.amazonaws.us-east-1.${service}`');
    expect(config).toContain('[["S3", "s3"], ["Dynamo", "dynamodb"]]');
    expect(config).toContain('vpcEndpointType: "Gateway"');
    expect(config).toContain('serviceName: "com.amazonaws.us-east-1.kms"');
    expect(config).toContain('vpcEndpointType: "Interface"');
    expect(config).not.toContain("nat:");
  });

  it("publishes the network the database sits in as a custom record, grantless", () => {
    expect(config).toContain(`link.custom(\n      "${CUSTOM_LINK_NAME}"`);
    expect(config).toContain("properties: { subnetIds: vpc.privateSubnets, securityGroupIds: vpc.securityGroups }");
    expect(config.slice(config.indexOf("link.custom("))).not.toContain("grants");
  });
});

describe("renderOcelConfig", () => {
  const config = renderOcelConfig({ slug: "e2es-1" });

  it("binds the published name through `links` and declares it as a resource", () => {
    expect(config).toContain('slug: "e2es-1"');
    expect(config).toContain(`links: ["${LINK_NAME}"]`);
    expect(config).not.toContain("mode:");
  });

  it("names the cloudflare edge, the only one this origin fronts deployments with", () => {
    expect(config).toContain(`import { cfEdge } from "ocel/edge";`);
    expect(config).toContain("edge: cfEdge()");
  });

  it("reads the custom record through a transform module, never through `links`", () => {
    expect(config).toContain(`transforms: ["${TRANSFORM_MODULE}"]`);
    expect(config).not.toContain(`"${CUSTOM_LINK_NAME}"`);
  });

  it("keeps the transform when the refusal leg unbinds every link, so it is the first thing missing", () => {
    const unbound = renderOcelConfig({ slug: "e2es-1", links: [] });
    expect(unbound).toContain("links: []");
    expect(unbound).toContain(`transforms: ["${TRANSFORM_MODULE}"]`);
  });
});

describe("publishedIds", () => {
  it("reads the comma-joined list an SST output carries", () => {
    expect(publishedIds("subnet-a,subnet-b")).toEqual(["subnet-a", "subnet-b"]);
    expect(publishedIds(" subnet-a , ,subnet-b ")).toEqual(["subnet-a", "subnet-b"]);
    expect(publishedIds(undefined)).toEqual([]);
  });
});

describe("vpcPlacementProblem", () => {
  const network = { subnetIds: ["subnet-a", "subnet-b"], securityGroupIds: ["sg-a"] };

  it("passes a function the transform placed in exactly the published network", () => {
    expect(
      vpcPlacementProblem(
        { VpcConfig: { SubnetIds: ["subnet-b", "subnet-a"], SecurityGroupIds: ["sg-a"] } },
        network,
      ),
    ).toBeNull();
  });

  it("names a function outside the VPC, or in the wrong part of it", () => {
    expect(vpcPlacementProblem({}, network)).toMatch(/subnets \[\]/);
    expect(
      vpcPlacementProblem({ VpcConfig: { SubnetIds: ["subnet-a"], SecurityGroupIds: ["sg-a"] } }, network),
    ).toMatch(/subnets/);
    expect(
      vpcPlacementProblem(
        { VpcConfig: { SubnetIds: ["subnet-a", "subnet-b"], SecurityGroupIds: ["sg-b"] } },
        network,
      ),
    ).toMatch(/security groups/);
  });

  it("refuses to pass against an empty published network, which would prove nothing", () => {
    expect(vpcPlacementProblem({}, { subnetIds: [], securityGroupIds: [] })).toMatch(/proves a placement/);
  });
});

describe("refusalProblem", () => {
  const refusal = [
    `a transform fills api-orders's vpc.securityGroupIds from link "${CUSTOM_LINK_NAME}"'s securityGroupIds,`,
    "and nothing has published a record under that name to production.",
  ].join(" ");

  it("passes a deploy that stopped at the transform site, naming the link it could not read", () => {
    expect(refusalProblem(1, refusal)).toBeNull();
  });

  it("names a deploy that went through with the record gone", () => {
    expect(refusalProblem(0, "")).toMatch(/exited 0/);
  });

  it("names a refusal that points somewhere else", () => {
    expect(refusalProblem(1, '`links` binds "orders", and nothing has published a record under that name')).toMatch(
      new RegExp(`the link ${CUSTOM_LINK_NAME}`),
    );
    expect(refusalProblem(1, `link "${CUSTOM_LINK_NAME}" is not published`)).toMatch(/vpc\.subnetIds/);
  });
});

describe("vpcAccessProblem", () => {
  const arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole";

  it("passes a role the deploy attached the VPC-access policy to", () => {
    expect(vpcAccessProblem([arn, "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"])).toBeNull();
  });

  it("names a role that would never attach a network interface", () => {
    expect(vpcAccessProblem([])).toMatch(/AWSLambdaVPCAccessExecutionRole/);
    expect(vpcAccessProblem(undefined)).toMatch(/AWSLambdaVPCAccessExecutionRole/);
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

  it("refuses to pass a record that grants nothing, which would prove nothing", () => {
    expect(grantsDeliveredProblem([{ Statement: [] }], [])).toMatch(/grants nothing/);
    expect(grantsDeliveredProblem([{ Statement: [] }], undefined)).toMatch(/grants nothing/);
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
  const row = (record, version = "1") => ({
    record: { S: JSON.stringify(record) },
    version: { N: version },
  });

  it("reads the redacted links.v1.Link the store keeps beside the sealed value", () => {
    const parsed = parsePublishedRecord(
      row({
        name: "orders",
        postgres: {},
        source: "sst",
        grants: [{ actions: ["rds-db:connect"], resources: ["arn:x"] }],
      }),
    );
    expect(parsed.type).toBe("postgres");
    expect(parsed.record.source).toBe("sst");
    expect(parsed.version).toBe(1);
  });

  it("names a record that is unparseable, nameless or typeless", () => {
    expect(parsePublishedRecord({ record: { S: "{" } }).problem).toMatch(/parseable/);
    expect(parsePublishedRecord({}).problem).toMatch(/no record/);
    expect(parsePublishedRecord(row({ postgres: {} })).problem).toMatch(/no name/);
    expect(parsePublishedRecord(row({ name: "orders" })).problem).toMatch(/no properties/);
  });
});

describe("recordShapeProblem", () => {
  it("passes the postgres link this suite publishes from SST", () => {
    expect(recordShapeProblem({ name: "orders", postgres: {}, source: "sst" })).toBeNull();
  });

  it("passes the custom link only a transform reads, and refuses it under the owned type", () => {
    expect(
      recordShapeProblem(
        { name: CUSTOM_LINK_NAME, custom: {}, source: "sst" },
        { name: CUSTOM_LINK_NAME, type: CUSTOM_LINK_TYPE },
      ),
    ).toBeNull();
    expect(
      recordShapeProblem(
        { name: CUSTOM_LINK_NAME, postgres: {}, source: "sst" },
        { name: CUSTOM_LINK_NAME, type: CUSTOM_LINK_TYPE },
      ),
    ).toMatch(/postgres/);
  });

  it("names a record published under another name, type or source", () => {
    expect(recordShapeProblem({ name: "other", postgres: {}, source: "sst" })).toMatch(/"other"/);
    expect(recordShapeProblem({ name: "orders", bucket: {}, source: "sst" })).toMatch(/bucket/);
    expect(recordShapeProblem({ name: "orders", postgres: {} })).toMatch(/sourced null/);
    expect(recordShapeProblem({ name: "orders", postgres: {}, source: "" })).toMatch(/ocel's own/);
  });
});

describe("redactionProblem", () => {
  it("passes a record whose properties the store stripped", () => {
    expect(redactionProblem({ name: "orders", postgres: {} })).toBeNull();
  });

  it("names a record row that kept a property in the clear, custom records included", () => {
    expect(redactionProblem({ name: "orders", postgres: { host: "h", password: "p" } })).toMatch(
      /host, password/,
    );
    expect(redactionProblem({ name: CUSTOM_LINK_NAME, custom: {} })).toBeNull();
    expect(redactionProblem({ name: CUSTOM_LINK_NAME, custom: { subnetIds: ["subnet-a"] } })).toMatch(
      /subnetIds/,
    );
    expect(redactionProblem({ name: "orders" })).toMatch(/no properties/);
  });
});

describe("listedLinkProblem", () => {
  const listed = [{ name: "orders", type: "postgres", source: "sst", owner: OWNER, version: 1 }];

  const customOwner = linkOwner({ app: "e2es-1", stage: "e2e", link: CUSTOM_LINK_NAME });
  const both = [
    ...listed,
    { name: CUSTOM_LINK_NAME, type: CUSTOM_LINK_TYPE, source: "sst", owner: customOwner, version: 1 },
  ];

  it("passes the one link `ocel link ls` reports for this project", () => {
    expect(listedLinkProblem(listed, { owner: OWNER })).toBeNull();
  });

  it("judges each published record apart, whatever else the project lists", () => {
    expect(listedLinkProblem(both, { owner: OWNER })).toBeNull();
    expect(
      listedLinkProblem(both, { name: CUSTOM_LINK_NAME, type: CUSTOM_LINK_TYPE, owner: customOwner }),
    ).toBeNull();
    expect(
      listedLinkProblem(listed, { name: CUSTOM_LINK_NAME, type: CUSTOM_LINK_TYPE, owner: customOwner }),
    ).toMatch(/want exactly one network/);
  });

  it("names a project listing another link, another type, source or owner", () => {
    expect(listedLinkProblem([], { owner: OWNER })).toMatch(/want exactly one orders/);
    expect(listedLinkProblem([...listed, ...listed], { owner: OWNER })).toMatch(/want exactly one orders/);
    expect(listedLinkProblem([{ ...listed[0], type: "bucket" }], { owner: OWNER })).toMatch(/bucket/);
    expect(listedLinkProblem([{ ...listed[0], source: "" }], { owner: OWNER })).toMatch(/source/);
    expect(listedLinkProblem([{ ...listed[0], owner: "cli" }], { owner: OWNER })).toMatch(/owned by cli/);
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

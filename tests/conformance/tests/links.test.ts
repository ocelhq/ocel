import { describe, expect, it } from "vitest";
import {
  customLinkName,
  grantsDeliveredProblem,
  linkIndexSortKey,
  linkOwner,
  linkPartitionKey,
  listedLinkProblem,
  pairProblem,
  publishedRecordProblem,
  recordSortKey,
  refusalProblem,
  valueSortKey,
  varsReachProblem,
  vpcPlacementProblem,
} from "../src/links";

describe("published link records", () => {
  it("accepts a redacted postgres record with a scoped grant", () => {
    expect(
      publishedRecordProblem(
        {
          record: {
            S: JSON.stringify({
              name: "orders",
              postgres: {},
              source: "sst",
              grants: [
                {
                  actions: ["rds-db:connect"],
                  resources: ["arn:aws:rds-db:us-east-1:123:dbuser:cluster/user"],
                },
              ],
            }),
          },
          version: { N: "4" },
          owner: { S: "urn:publisher" },
        },
        {
          name: "orders",
          type: "postgres",
          source: "sst",
          owner: "urn:publisher",
        },
      ),
    ).toBeNull();
  });

  it("rejects a cleartext value, the wrong owner, and broad grants", () => {
    const record = {
      record: {
        S: JSON.stringify({
          name: "orders",
          postgres: { password: "clear" },
          source: "pulumi",
          grants: [{ actions: ["rds-db:*"], resources: ["*"] }],
        }),
      },
      owner: { S: "other" },
    };
    expect(
      publishedRecordProblem(record, {
        name: "orders",
        type: "postgres",
        source: "pulumi",
        owner: "urn:publisher",
      }),
    ).toMatch(/stamped other/);
    record.owner.S = "urn:publisher";
    expect(
      publishedRecordProblem(record, {
        name: "orders",
        type: "postgres",
        source: "pulumi",
        owner: "urn:publisher",
      }),
    ).toMatch(/password in the clear/);
  });

  it("requires a matched encrypted record and value pair", () => {
    expect(
      pairProblem(
        { version: { N: "2" } },
        { version: { N: "2" }, ciphertext: { B: "sealed" } },
      ),
    ).toBeNull();
    expect(
      pairProblem({ version: { N: "2" } }, { version: { N: "3" } }),
    ).toMatch(/versions/);
    expect(pairProblem({ version: { N: "2" } })).toMatch(/without the value/);
  });

  it("addresses the preview environment and both tool owner shapes", () => {
    expect(linkPartitionKey("e2ec-1", "orders")).toBe(
      "PROJECT#e2ec-1#CLASS#preview#LINK#orders",
    );
    expect(recordSortKey("conformance-with-sst")).toBe(
      "RECORD#ENV#conformance-with-sst",
    );
    expect(valueSortKey("conformance-with-sst")).toBe(
      "VALUE#FOLDER#/#NAME#PROPERTIES#ENV#conformance-with-sst",
    );
    expect(linkIndexSortKey("urn:owner", "conformance-with-sst")).toBe(
      "LINKS#OWNER#urn:owner#ENV#conformance-with-sst",
    );
    expect(linkOwner("sst", "with-sst", "e2e", "orders")).toBe(
      "urn:pulumi:e2e::with-sst::pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource::ocel-link-orders",
    );
    expect(linkOwner("pulumi", "with-pulumi", "e2e", "orders")).toBe(
      "urn:pulumi:e2e::with-pulumi::pulumi-nodejs:dynamic:Resource::ocel-link-orders",
    );
  });

  it("checks link listing identity", () => {
    const expected = {
      name: customLinkName,
      type: "custom" as const,
      source: "sst" as const,
      owner: "urn:network",
    };
    expect(
      listedLinkProblem(
        [
          {
            name: customLinkName,
            type: "custom",
            source: "sst",
            owner: "urn:network",
          },
        ],
        expected,
      ),
    ).toBeNull();
    expect(listedLinkProblem([], expected)).toMatch(/0 records/);
  });
});

describe("consumed links", () => {
  const grants = [
    {
      actions: ["rds-db:connect"],
      resources: ["arn:aws:rds-db:us-east-1:123:dbuser:cluster/user"],
    },
  ];

  it("requires the published grant and value partition on the role", () => {
    const documents = [
      {
        Statement: [
          {
            Effect: "Allow",
            Action: "rds-db:connect",
            Resource: "arn:aws:rds-db:us-east-1:123:dbuser:cluster/user",
          },
          {
            Effect: "Allow",
            Action: "dynamodb:Query",
            Resource: "arn:aws:dynamodb:us-east-1:123:table/vars",
            Condition: {
              "ForAllValues:StringEquals": {
                "dynamodb:LeadingKeys": [
                  "PROJECT#e2ec-1#CLASS#preview#LINK#orders",
                ],
              },
            },
          },
        ],
      },
    ];
    expect(grantsDeliveredProblem(documents, grants)).toBeNull();
    expect(
      varsReachProblem(documents, linkPartitionKey("e2ec-1", "orders")),
    ).toBeNull();
    expect(grantsDeliveredProblem([], grants)).toMatch(/no inline policy/);
  });

  it("matches the function placement to the published network", () => {
    const outputs = {
      host: "host",
      port: "5432",
      database: "orders",
      subnetIds: ["subnet-b", "subnet-a"],
      securityGroupIds: ["sg-a"],
    };
    expect(
      vpcPlacementProblem(
        {
          VpcConfig: {
            SubnetIds: ["subnet-a", "subnet-b"],
            SecurityGroupIds: ["sg-a"],
          },
        },
        outputs,
      ),
    ).toBeNull();
    expect(vpcPlacementProblem({}, outputs)).toMatch(/subnets/);
  });
});

describe("refused links", () => {
  it("requires the failed deploy to name the missing transform input", () => {
    expect(
      refusalProblem(
        1,
        'a transform fills api vpc.subnetIds from link "network", and nothing has published a record under that name',
      ),
    ).toBeNull();
    expect(refusalProblem(0, "")).toMatch(/succeeded/);
    expect(refusalProblem(1, 'link "orders" is missing')).toMatch(/network/);
  });
});

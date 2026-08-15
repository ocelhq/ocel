import { describe, expect, it } from "vitest";
import { grantsFor, recordFor, tokenFor } from "./record.js";

const postgresUrn =
  "urn:pulumi:production::acme::sst:aws:Postgres$aws:rds/cluster:Cluster::Orders";

describe("tokenFor", () => {
  it("namespaces the component's own type after its publisher", () => {
    expect(tokenFor("urn:pulumi:production::acme::sst:aws:Postgres::Orders")).toBe(
      "sst:aws.Postgres",
    );
  });

  it("refuses a constituent rather than publishing it as the composite", () => {
    expect(() => tokenFor(postgresUrn)).toThrow(/aws:rds\/cluster:Cluster/);
    expect(() => tokenFor(postgresUrn)).toThrow(/sst:aws:Postgres/);
  });

  it("refuses a urn that names no type", () => {
    expect(() => tokenFor("urn:pulumi:production::acme")).toThrow(/type/i);
  });
});

describe("recordFor", () => {
  const link = {
    name: "orders",
    urn: "urn:pulumi:production::acme::sst:aws:Postgres::Orders",
    properties: {
      host: "orders.cluster-abc.us-east-1.rds.amazonaws.com",
      port: 5432,
      database: "orders",
      username: "app",
      password: "hunter2",
    },
  };

  it("carries the component's own properties as a flat string bag", () => {
    expect(recordFor(link)).toEqual({
      name: "orders",
      type: "sst:aws.Postgres",
      properties: {
        host: "orders.cluster-abc.us-east-1.rds.amazonaws.com",
        port: "5432",
        database: "orders",
        username: "app",
        password: "hunter2",
      },
    });
  });

  it("drops a property the component left unset", () => {
    const record = recordFor({
      ...link,
      properties: { host: "h", tls: undefined, replica: null },
    });
    expect(Object.keys(record.properties)).toEqual(["host"]);
  });

  it("refuses a property no consumer could read as a string", () => {
    for (const properties of [
      { pool: { max: 10 } },
      { pool: [1, 2] },
      { pool: () => 10 },
      { pool: Symbol("pool") },
    ]) {
      expect(() => recordFor({ ...link, properties })).toThrow(/pool/);
    }
  });

  it("refuses a token in the ocel namespace", () => {
    expect(() =>
      recordFor({
        ...link,
        urn: "urn:pulumi:production::acme::ocel:aws:Postgres::Orders",
      }),
    ).toThrow(/ocel:/);
  });

  it("carries scoped grants through", () => {
    const record = recordFor({
      ...link,
      grants: [
        {
          actions: ["rds-db:connect"],
          resources: ["arn:aws:rds-db:us-east-1:1234:dbuser:db-ORDERS/app"],
          label: "connect",
        },
      ],
    });
    expect(record.grants).toHaveLength(1);
    expect(record.grants?.[0]?.label).toBe("connect");
  });

  it("refuses a grant that reaches past the resource it links", () => {
    for (const grant of [
      { actions: ["s3:*"], resources: ["arn:aws:s3:::acme/*"] },
      { actions: ["s3:GetObject"], resources: ["*"] },
      { actions: [], resources: ["arn:aws:s3:::acme/*"] },
      { actions: ["s3:GetObject"], resources: [] },
    ]) {
      expect(() => recordFor({ ...link, grants: [grant] })).toThrow(/grant/i);
    }
  });

  it("refuses a link with no name", () => {
    expect(() => recordFor({ ...link, name: "" })).toThrow(/name/i);
  });
});

describe("grantsFor", () => {
  it("translates sst's own included permissions into grants", () => {
    expect(
      grantsFor("invoices", [
        {
          type: "aws.permission",
          actions: ["s3:GetObject"],
          resources: ["arn:aws:s3:::acme/*"],
        },
      ]),
    ).toEqual([
      { actions: ["s3:GetObject"], resources: ["arn:aws:s3:::acme/*"] },
    ]);
  });

  it("has nothing to translate when the link includes nothing", () => {
    expect(grantsFor("invoices", undefined)).toBeUndefined();
    expect(grantsFor("invoices", [])).toBeUndefined();
  });

  it("refuses an include it cannot express as a grant", () => {
    expect(() =>
      grantsFor("invoices", [{ type: "aws.something", actions: ["s3:Get"] }]),
    ).toThrow(/aws.something/);
    expect(() =>
      grantsFor("invoices", [
        {
          type: "aws.permission",
          effect: "deny",
          actions: ["s3:GetObject"],
          resources: ["arn:aws:s3:::acme/*"],
        },
      ]),
    ).toThrow(/deny/);
  });
});

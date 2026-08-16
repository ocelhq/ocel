import { describe, expect, it } from "vitest";
import { coordinateFor, requestFor, type PublisherInputs } from "./publisher.js";

const orders = {
  urn: "urn:pulumi:production::acme::sst:aws:Postgres::Orders",
  properties: { host: "h", port: 5432, database: "orders" },
};

const invoices = {
  urn: "urn:pulumi:production::acme::sst:aws:Bucket::Invoices",
  properties: { name: "acme-invoices" },
  grants: [
    {
      actions: ["s3:GetObject"],
      resources: ["arn:aws:s3:::acme-invoices/*"],
      label: "read",
    },
  ],
};

const inputs: PublisherInputs = {
  project: "acme",
  class: "production",
  instance: "OcelLinks",
  links: { orders, invoices },
};

describe("requestFor", () => {
  it("addresses an ocel coordinate under the project it is given", () => {
    const request = requestFor(inputs);
    expect(request.project).toBe("acme");
    expect(request.publisher).toBe("sst:OcelLinks");
    expect(request.class).toBe("production");
    expect(request.environment).toBeUndefined();
  });

  it("names the instance, so two publishers never prune each other's records", () => {
    expect(requestFor({ ...inputs, instance: "Other" }).publisher).toBe(
      "sst:Other",
    );
    expect(() => requestFor({ ...inputs, instance: undefined })).toThrow(
      /instance/i,
    );
    expect(() => requestFor({ ...inputs, instance: "a#b" })).toThrow(/#/);
  });

  it("publishes one record per consumable resource, named by its binding", () => {
    const request = requestFor(inputs);
    expect(request.records?.map((r) => r.name).sort()).toEqual([
      "invoices",
      "orders",
    ]);
  });

  it("refuses a constituent rather than publishing it as the composite", () => {
    expect(() =>
      requestFor({
        ...inputs,
        links: {
          orders: {
            urn: "urn:pulumi:production::acme::sst:aws:Postgres$aws:rds/cluster:Cluster::Orders",
            properties: { endpoint: "cluster.rds" },
          },
        },
      }),
    ).toThrow(/aws:rds\/cluster:Cluster/);
  });

  it("takes an sst component through the link interface it already implements", () => {
    const component = {
      urn: orders.urn,
      getSSTLink: () => ({ properties: orders.properties }),
    };
    const request = requestFor({ ...inputs, links: { orders: component } });
    expect(request.records?.[0]?.properties.database).toBe("orders");
  });

  it("carries sst's own included permissions through as grants", () => {
    const component = {
      urn: invoices.urn,
      getSSTLink: () => ({
        properties: { name: "acme-invoices" },
        include: [
          {
            type: "aws.permission",
            actions: ["s3:GetObject"],
            resources: ["arn:aws:s3:::acme-invoices/*"],
          },
        ],
      }),
    };
    const request = requestFor({ ...inputs, links: { invoices: component } });
    expect(request.records?.[0]?.grants).toEqual([
      {
        actions: ["s3:GetObject"],
        resources: ["arn:aws:s3:::acme-invoices/*"],
      },
    ]);
  });

  it("refuses sst's permissive default rather than dropping it", () => {
    const component = {
      urn: invoices.urn,
      getSSTLink: () => ({
        properties: { name: "acme-invoices" },
        include: [
          {
            type: "aws.permission",
            actions: ["s3:*"],
            resources: ["arn:aws:s3:::acme-invoices/*"],
          },
        ],
      }),
    };
    expect(() =>
      requestFor({ ...inputs, links: { invoices: component } }),
    ).toThrow(/grant/i);
  });

  it("targets a named preview environment when it is given one", () => {
    const request = requestFor({
      ...inputs,
      class: "preview",
      environment: "pr-9",
    });
    expect(request.class).toBe("preview");
    expect(request.environment).toBe("pr-9");
  });

  it("refuses the class-wide marker as an environment name", () => {
    expect(() =>
      requestFor({ ...inputs, class: "preview", environment: "*" }),
    ).toThrow(/\*/);
  });

  it("refuses an environment named outside the preview class", () => {
    expect(() => requestFor({ ...inputs, environment: "pr-9" })).toThrow(
      /preview/,
    );
  });

  it("refuses a publisher with no project to address", () => {
    expect(() => requestFor({ ...inputs, project: "" })).toThrow(/project/i);
  });

  it("refuses a class it has no coordinate for", () => {
    expect(() =>
      requestFor({ ...inputs, class: "staging" as "production" }),
    ).toThrow(/staging/);
  });

  it("refuses a link the ocel namespace already speaks for", () => {
    expect(() =>
      requestFor({
        ...inputs,
        links: {
          orders: {
            urn: "urn:pulumi:production::acme::ocel:aws:Postgres::Orders",
            properties: { host: "h" },
          },
        },
      }),
    ).toThrow(/ocel:/);
  });
});

describe("coordinateFor", () => {
  it("names a coordinate a destroy can prune without re-reading a resource", () => {
    const coordinate = coordinateFor({
      project: "acme",
      class: "production",
      instance: "OcelLinks",
    });
    expect(coordinate.records).toBeUndefined();
    expect(coordinate.project).toBe("acme");
    expect(coordinate.publisher).toBe("sst:OcelLinks");
  });

  it("tells two instances of one coordinate apart", () => {
    expect(
      coordinateFor({ ...inputs, instance: "Other" }).publisher,
    ).not.toBe(coordinateFor(inputs).publisher);
  });
});

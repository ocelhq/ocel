import { existsSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { source } from "./cli.js";
import { postgresBinding } from "./postgres.js";

function repoRoot() {
  let dir = new URL("./", import.meta.url);
  for (;;) {
    if (existsSync(new URL("go.work", dir))) return dir;
    const parent = new URL("../", dir);
    if (parent.href === dir.href) {
      throw new Error("no go.work found above the sst package");
    }
    dir = parent;
  }
}

const fixture = JSON.parse(
  readFileSync(new URL("proto/common/bindings/v1/fixtures/postgres.json", repoRoot()), "utf8"),
);

function sstPostgres(properties: Record<string, unknown>) {
  return { getSSTBinding: () => ({ properties }) };
}

describe("the record a postgres binding publishes as", () => {
  it("is the conformance fixture, sourced to sst", () => {
    const resource = sstPostgres({
      database: fixture.postgres.database,
      username: fixture.postgres.username,
      password: fixture.postgres.password,
      port: fixture.postgres.port,
      host: fixture.postgres.host,
    });

    expect(postgresBinding(fixture.name, resource.getSSTBinding())).toEqual({
      ...fixture,
      source,
    });
  });

  it("turns an sst include into grants", () => {
    const binding = postgresBinding("orders", {
      properties: fixture.postgres,
      include: [
        {
          type: "aws.permission",
          actions: ["rds-db:connect"],
          resources: ["arn:aws:rds-db:us-east-1:1:dbuser:cluster/operator"],
        },
      ],
    });

    expect(binding.grants).toEqual([
      {
        actions: ["rds-db:connect"],
        resources: ["arn:aws:rds-db:us-east-1:1:dbuser:cluster/operator"],
      },
    ]);
  });

  it("refuses a grant reaching past the resource the binding names", () => {
    expect(() =>
      postgresBinding("orders", {
        properties: fixture.postgres,
        grants: [{ actions: ["rds-db:connect"], resources: ["*"] }],
      }),
    ).toThrow(/reaches past|nothing else/);
  });

  it("refuses a resource missing a field an app resolving it reads", () => {
    const { password, ...rest } = fixture.postgres;
    expect(() => postgresBinding("orders", { properties: rest })).toThrow(/carries no password/);
  });

  it("refuses a port that is not a whole number", () => {
    expect(() =>
      postgresBinding("orders", {
        properties: { ...fixture.postgres, port: "not-a-port" },
      }),
    ).toThrow(/port/);
  });

  it("reads a port SST handed over as a string", () => {
    const binding = postgresBinding("orders", {
      properties: { ...fixture.postgres, port: "5433" },
    });

    expect(binding.postgres.port).toBe(5433);
  });
});

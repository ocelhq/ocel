import { existsSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { source } from "./cli.js";
import { postgresLink } from "./postgres.js";

function repoRoot() {
  let dir = new URL("./", import.meta.url);
  for (;;) {
    if (existsSync(new URL("go.work", dir))) return dir;
    const parent = new URL("../", dir);
    if (parent.href === dir.href) {
      throw new Error("no go.work found above the pulumi package");
    }
    dir = parent;
  }
}

const fixture = JSON.parse(
  readFileSync(
    new URL("proto/links/v1/fixtures/postgres.json", repoRoot()),
    "utf8",
  ),
);

describe("the record a postgres link publishes as", () => {
  it("is the conformance fixture, sourced to pulumi", () => {
    expect(
      postgresLink(fixture.name, {
        properties: {
          database: fixture.postgres.database,
          username: fixture.postgres.username,
          password: fixture.postgres.password,
          port: fixture.postgres.port,
          host: fixture.postgres.host,
        },
      }),
    ).toEqual({ ...fixture, source });
  });

  it("carries the grants it was handed", () => {
    const link = postgresLink("orders", {
      properties: fixture.postgres,
      grants: [
        {
          actions: ["rds-db:connect"],
          resources: ["arn:aws:rds-db:us-east-1:1:dbuser:cluster/operator"],
        },
      ],
    });

    expect(link.grants).toEqual([
      {
        actions: ["rds-db:connect"],
        resources: ["arn:aws:rds-db:us-east-1:1:dbuser:cluster/operator"],
      },
    ]);
  });

  it("refuses a grant reaching past the resource the link names", () => {
    expect(() =>
      postgresLink("orders", {
        properties: fixture.postgres,
        grants: [{ actions: ["rds-db:connect"], resources: ["*"] }],
      }),
    ).toThrow(/reaches past|nothing else/);
  });

  it("refuses a grant over a whole service", () => {
    expect(() =>
      postgresLink("orders", {
        properties: fixture.postgres,
        grants: [
          { actions: ["rds-db:*"], resources: ["arn:aws:rds-db:us-east-1:1:x"] },
        ],
      }),
    ).toThrow(/reaches past/);
  });

  it("refuses a resource missing a field an app resolving it reads", () => {
    const { password, ...rest } = fixture.postgres;
    expect(() => postgresLink("orders", { properties: rest })).toThrow(
      /carries no password/,
    );
  });

  it("refuses a port that is not a whole number", () => {
    expect(() =>
      postgresLink("orders", {
        properties: { ...fixture.postgres, port: "not-a-port" },
      }),
    ).toThrow(/port/);
  });

  it("reads a port a provider handed over as a string", () => {
    const link = postgresLink("orders", {
      properties: { ...fixture.postgres, port: "5433" },
    });

    expect(link.postgres.port).toBe(5433);
  });

  it("refuses a record under no name", () => {
    expect(() =>
      postgresLink("", { properties: fixture.postgres }),
    ).toThrow(/published under no name/);
  });
});

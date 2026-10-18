import { existsSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { source } from "./cli.js";
import { publishedPostgres } from "./postgres.js";

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
    new URL("proto/provider/variablestore/v1/fixtures/postgres.json", repoRoot()),
    "utf8",
  ),
);
const record = fixture.binding;

describe("the record a postgres binding publishes as", () => {
  it("is the conformance fixture less its tls settings, sourced to pulumi", () => {
    const { tlsMode, tlsCa, ...published } = record.postgres;

    expect(
      publishedPostgres(record.name, {
        properties: {
          database: record.postgres.database,
          username: record.postgres.username,
          password: record.postgres.password,
          port: record.postgres.port,
          host: record.postgres.host,
        },
        grants: fixture.grants,
      }),
    ).toEqual({ binding: { ...record, postgres: published, source }, grants: fixture.grants });
  });

  it("keeps the grants it was handed", () => {
    const binding = publishedPostgres("orders", {
      properties: record.postgres,
      grants: [
        {
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
      publishedPostgres("orders", {
        properties: record.postgres,
        grants: [{ actions: ["rds-db:connect"], resources: ["*"] }],
      }),
    ).toThrow(/reaches past|nothing else/);
  });

  it("refuses a grant over a whole service", () => {
    expect(() =>
      publishedPostgres("orders", {
        properties: record.postgres,
        grants: [{ actions: ["rds-db:*"], resources: ["arn:aws:rds-db:us-east-1:1:x"] }],
      }),
    ).toThrow(/reaches past/);
  });

  it("refuses a resource missing a field an app resolving it reads", () => {
    const { password, ...rest } = record.postgres;
    expect(() => publishedPostgres("orders", { properties: rest })).toThrow(/has no password/);
  });

  it("refuses a port that is not a whole number", () => {
    expect(() =>
      publishedPostgres("orders", {
        properties: { ...record.postgres, port: "not-a-port" },
      }),
    ).toThrow(/port/);
  });

  it("reads a port a provider handed over as a string", () => {
    const binding = publishedPostgres("orders", {
      properties: { ...record.postgres, port: "5433" },
    });

    expect(binding.binding.postgres.port).toBe(5433);
  });

  it("refuses a record under no name", () => {
    expect(() => publishedPostgres("", { properties: record.postgres })).toThrow(
      /published under no name/,
    );
  });
});

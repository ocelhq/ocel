import { describe, expect, it } from "vitest";

import { defineConfig } from "./config.js";
import { exec, infisical } from "./env-source.js";

const roundTrip = (value: unknown) => JSON.parse(JSON.stringify(value));

describe("infisical", () => {
  it("serialises its options keyed by infisical", () => {
    expect(roundTrip(infisical({ project: "p-1", environment: "prod" }))).toEqual({
      infisical: { project: "p-1", environment: "prod" },
    });
  });

  it("carries the machine identity a deployed tier signs in as", () => {
    expect(
      roundTrip(
        infisical({
          project: "p-1",
          environment: "prod",
          path: "/acme",
          write: "missing",
          auth: {
            universal: {
              clientId: { var: "INFISICAL_CLIENT_ID" },
              clientSecret: { var: "INFISICAL_CLIENT_SECRET" },
            },
          },
        }),
      ),
    ).toEqual({
      infisical: {
        project: "p-1",
        environment: "prod",
        path: "/acme",
        write: "missing",
        auth: {
          universal: {
            clientId: { var: "INFISICAL_CLIENT_ID" },
            clientSecret: { var: "INFISICAL_CLIENT_SECRET" },
          },
        },
      },
    });
  });
});

describe("exec", () => {
  it("serialises the command and the format it prints, keyed by exec", () => {
    expect(roundTrip(exec({ command: ["op", "inject", "{folder}"], format: "dotenv" }))).toEqual({
      exec: { command: ["op", "inject", "{folder}"], format: "dotenv" },
    });
  });
});

describe("an envSource built from the helpers", () => {
  it("serialises every tier it names", () => {
    const config = defineConfig({
      slug: "test-app",
      envSource: {
        production: infisical({
          project: "p-1",
          environment: "prod",
          auth: { aws: { identityId: "ident" } },
        }),
        preview: "builtin",
        dev: exec({ command: ["./scripts/dev-env.sh"], format: "json" }),
      },
    });

    expect(roundTrip(config.envSource)).toEqual({
      production: {
        infisical: { project: "p-1", environment: "prod", auth: { aws: { identityId: "ident" } } },
      },
      preview: "builtin",
      dev: { exec: { command: ["./scripts/dev-env.sh"], format: "json" } },
    });
  });
});
